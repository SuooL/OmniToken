// Package proxy implements F14: a local LLM API forwarding proxy.
//
// It sits beside collect in the layering — a collection mechanism with an
// injected sink — because both the agent and the server host it: the agent
// pushes what it observes over HTTP, the server writes straight to its store.
// It lived inside internal/agent until the server needed it too, and a
// server→agent import would have been a sideways edge between two siblings.
//
// Scripts point their base_url at http://127.0.0.1:8899/<prefix> (e.g.
// /anthropic, /openai); the proxy strips the prefix and forwards the request
// verbatim (method, headers incl. Authorization, body) to the configured
// upstream. While forwarding it tees the response stream to measure precise
// TTFT / total duration and to extract token usage — the one blind spot of
// log-based collection (ADR-0001). The proxy NEVER alters request or response
// content; a usage-parse failure still forwards the full stream and still
// emits a timing-only event.
package proxy

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Source is the Event.Source value for proxy-observed usage.
const Source = "proxy"

const (
	proxyRingCap    = 1000    // events buffered across sink failures
	proxyCaptureMax = 4 << 20 // non-stream response bytes kept for usage parsing
)

// defaultUpstreams are always available; Config.Upstreams overrides/extends.
var defaultUpstreams = map[string]string{
	"anthropic": "https://api.anthropic.com",
	"openai":    "https://api.openai.com",
}

// Config configures the local forwarding proxy.
type Config struct {
	Listen    string            // e.g. "127.0.0.1:8899"
	Device    string            // device name stamped on emitted events
	Upstreams map[string]string // path prefix -> upstream base URL; merged over defaults
}

// Run runs the forwarding proxy until the listener fails. It blocks;
// callers run it in a goroutine. sink has the same shape as the agent's push
// sink; failed batches are kept in an in-memory ring (cap ~1000) and re-sent
// together with the next event, so transient server outages lose nothing and
// never block the forwarding path.
func Run(cfg Config, sink func([]model.Event) error) error {
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newProxyHandler(cfg, sink),
		ReadHeaderTimeout: 10 * time.Second,
		// No Write/Idle timeouts: streamed generations can run for minutes.
	}
	log.Printf("proxy: listening on %s", cfg.Listen)
	return srv.ListenAndServe()
}

type proxyHandler struct {
	device    string
	upstreams map[string]string
	client    *http.Client
	emitter   *proxyEmitter
	seq       atomic.Uint64
}

func newProxyHandler(cfg Config, sink func([]model.Event) error) *proxyHandler {
	ups := make(map[string]string, len(defaultUpstreams)+len(cfg.Upstreams))
	for k, v := range defaultUpstreams {
		ups[k] = v
	}
	for k, v := range cfg.Upstreams {
		ups[strings.Trim(k, "/")] = v
	}
	return &proxyHandler{
		device:    cfg.Device,
		upstreams: ups,
		// Timeout stays 0: an overall deadline would kill long streams. The
		// client's own disconnect cancels r.Context() and aborts the upstream
		// call, so nothing leaks.
		client:  &http.Client{Timeout: 0},
		emitter: &proxyEmitter{sink: sink},
	}
}

// Hop-by-hop headers are stripped per RFC 9110. Accept-Encoding is stripped
// too so Go's transport negotiates gzip itself and transparently decompresses,
// keeping the teed bytes parseable; the client receives identity-encoded
// content with unchanged semantics.
var proxyHopHeaders = map[string]bool{
	"Connection":        true,
	"Proxy-Connection":  true,
	"Keep-Alive":        true,
	"Te":                true,
	"Trailer":           true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
	"Accept-Encoding":   true,
}

func (p *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix, rest := splitProxyPrefix(r.URL.Path)
	base, ok := p.upstreams[prefix]
	if !ok {
		writeProxyErr(w, http.StatusNotFound, "unknown upstream prefix: "+prefix)
		return
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		writeProxyErr(w, http.StatusBadGateway, "bad upstream base for "+prefix+": "+base)
		return
	}

	// Read the body fully so the model field can be extracted, then replay it
	// verbatim to the upstream.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeProxyErr(w, http.StatusBadGateway, "read request body: "+err.Error())
		return
	}
	reqModel := extractProxyModel(body)

	target := *baseURL
	target.Path = strings.TrimSuffix(baseURL.Path, "/") + rest
	target.RawQuery = r.URL.RawQuery

	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeProxyErr(w, http.StatusBadGateway, "build upstream request: "+err.Error())
		return
	}
	for k, vv := range r.Header {
		if proxyHopHeaders[k] {
			continue
		}
		out.Header[k] = vv
	}
	out.ContentLength = int64(len(body))

	start := time.Now()
	resp, err := p.client.Do(out)
	if err != nil {
		writeProxyErr(w, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		if proxyHopHeaders[k] {
			continue
		}
		w.Header()[k] = vv
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush() // headers out immediately — matters for SSE TTFT downstream
	}

	obs := newUsageObserver(strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"))
	var ttft time.Duration
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			obs.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				break // client gone; stop relaying, emit what we observed
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	duration := time.Since(start)

	u, obsModel, msgID := obs.result()
	m := reqModel
	if m == "" {
		m = obsModel
	}
	ev := model.Event{
		TS:                  start.UnixMilli(),
		Device:              p.device,
		Source:              Source,
		Model:               m,
		Provider:            proxyProvider(prefix, r.Header),
		AccountLabel:        proxyAccountLabel(r.Header),
		InputTokens:         u.input,
		OutputTokens:        u.output,
		CacheReadTokens:     u.cacheRead,
		CacheCreationTokens: u.cacheCreate,
		// The measured span IS the generation interval, which is what gen_ms
		// means (ADR-0009) — better than the estimate a log can offer.
		GenMS:  duration.Milliseconds(),
		TTFTMS: ttft.Milliseconds(),
		// CWD intentionally empty: the proxy cannot know the caller's cwd.
	}
	// Same request, same id: when the log will describe this request too, the
	// two observations must collapse into one row rather than count twice
	// (ADR-0013). Failing that, this is a standalone observation.
	if shared := sharedEventID(prefix, msgID, resp.Header); shared != "" {
		ev.EventID = shared
	} else {
		ev.EventID = proxyEventID(p.device, prefix, start.UnixNano(), p.seq.Add(1))
		// duration_ms means "gap to the previous log record" on rows a log
		// channel owns (ADR-0006, F8 work time). Only a row with no log twin
		// can carry the measured span there without changing that meaning.
		ev.DurationMS = duration.Milliseconds()
	}
	// Async so a slow sink never delays finishing the client's response.
	go p.emitter.emit(ev)
}

// splitProxyPrefix splits "/anthropic/v1/messages" into ("anthropic", "/v1/messages").
func splitProxyPrefix(path string) (prefix, rest string) {
	p := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, "/"
}

// proxyProvider maps a request onto an Event.Provider label (ADR-0005's real
// vs equivalent cost split depends on it).
//
// This used to hard-code the "-api" labels on the assumption that traffic
// through the proxy always carries an API key. That is not true: pointing a
// subscription tool's base URL at the proxy forwards its OAuth bearer token
// just as verbatim as anything else, and billing it as real dollars would be a
// straightforward accounting error.
//
// So the credential's shape decides, and when it says nothing the label stays
// the undetermined first-party one — which ADR-0005 counts as equivalent value.
// Guessing "API key" costs real money on the report; guessing "subscription"
// only understates a figure that is already labelled an estimate.
func proxyProvider(prefix string, h http.Header) string {
	switch prefix {
	case "anthropic":
		switch credentialKind(h) {
		case credAPIKey:
			return "anthropic-api"
		case credOAuth:
			return "anthropic-oauth"
		}
		return "anthropic"
	case "openai":
		switch credentialKind(h) {
		case credAPIKey:
			return "openai-api"
		case credOAuth:
			return model.ProviderOpenAIChatGPT // Codex on a ChatGPT plan
		}
		return "openai"
	default:
		return prefix
	}
}

const (
	credAPIKey = "api-key"
	credOAuth  = "oauth"
)

// credentialKind reads the billing channel off the credential's shape, without
// keeping the credential.
//
// The shapes were taken from this machine rather than from memory:
//
//   - Anthropic subscription: Authorization: Bearer sk-ant-oat<...> (the token
//     Claude Code stores under claudeAiOauth in the login keychain item);
//   - Anthropic API key: sent in x-api-key, and shaped sk-ant-api<...>;
//   - ChatGPT-plan Codex: Authorization: Bearer <JWT>, i.e. starts with "eyJ"
//     (~/.codex/auth.json has auth_mode=chatgpt, a null OPENAI_API_KEY and JWT
//     tokens);
//   - OpenAI API key: Authorization: Bearer sk-<...> / sk-proj-<...>.
//
// An unrecognised shape returns "" so the caller stays undetermined instead of
// inventing a billing channel.
func credentialKind(h http.Header) string {
	if h.Get("x-api-key") != "" {
		return credAPIKey // Anthropic's API-key header; OAuth never uses it
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
	switch {
	case tok == "":
		return ""
	case strings.HasPrefix(tok, "sk-ant-oat"):
		return credOAuth
	case strings.HasPrefix(tok, "eyJ"): // JWT — how ChatGPT plans authenticate
		return credOAuth
	case strings.HasPrefix(tok, "sk-"):
		return credAPIKey
	}
	return ""
}

// proxyAccountLabel fingerprints the credential without ever storing it:
// SHA-256 of the raw Authorization (or x-api-key) header value, first 12 hex
// — enough to tell accounts apart, and one-way even against a candidate-key
// list. The plaintext key never leaves this function.
func proxyAccountLabel(h http.Header) string {
	key := h.Get("Authorization")
	if key == "" {
		key = h.Get("x-api-key")
	}
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// sharedEventID reproduces the id the Claude Code parser derives for this same
// request, or "" when the request cannot be identified that way (ADR-0013).
//
// Both halves come off the wire: the message id from the response body or the
// message_start event, the request id from Anthropic's response header. The
// format has to stay in lockstep with parser/claudecode.eventID — a regression
// test pins the two together.
func sharedEventID(prefix, msgID string, respHeader http.Header) string {
	if prefix != "anthropic" || msgID == "" {
		return ""
	}
	reqID := respHeader.Get("request-id")
	if reqID == "" {
		reqID = respHeader.Get("anthropic-request-id")
	}
	if reqID == "" {
		return ""
	}
	return "cc:" + msgID + ":" + reqID
}

func proxyEventID(device, prefix string, startNano int64, seq uint64) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%d", device, prefix, startNano, seq)))
	return "px:" + hex.EncodeToString(sum[:12])
}

func extractProxyModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return v.Model
}

func writeProxyErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- usage observation (tee-side parsing; never touches the forwarded bytes) ----

// proxyWireUsage covers both provider dialects; which fields are set decides
// the semantics (see merge).
type proxyWireUsage struct {
	// Anthropic dialect (input_tokens EXCLUDES cache reads/writes)
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// OpenAI dialect (prompt_tokens INCLUDES cached_tokens)
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type proxyTokens struct {
	input, output, cacheRead, cacheCreate int64
}

// usageObserver incrementally extracts usage from the teed response stream.
// SSE responses are parsed line by line as chunks arrive (so an arbitrarily
// long stream needs O(line) memory); non-stream responses are captured up to
// proxyCaptureMax and parsed once at the end.
type usageObserver struct {
	sse   bool
	raw   bytes.Buffer // non-SSE capture
	line  bytes.Buffer // SSE partial-line assembly
	acc   proxyTokens
	model string
	msgID string // Anthropic message id, half of the shared event_id (ADR-0013)
}

func newUsageObserver(sse bool) *usageObserver {
	return &usageObserver{sse: sse}
}

func (o *usageObserver) feed(b []byte) {
	if !o.sse {
		if o.raw.Len() < proxyCaptureMax {
			o.raw.Write(b)
		}
		return
	}
	o.line.Write(b)
	for {
		data := o.line.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		line := string(data[:i])
		o.line.Next(i + 1)
		o.sseLine(strings.TrimRight(line, "\r"))
	}
}

// sseLine handles one SSE line; only "data:" payloads matter.
func (o *usageObserver) sseLine(line string) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(line[len("data:"):])
	if payload == "" || payload == "[DONE]" {
		return
	}
	var ev struct {
		Model   string `json:"model"`
		Message *struct {
			ID    string          `json:"id"` // half of the shared event_id (ADR-0013)
			Model string          `json:"model"`
			Usage *proxyWireUsage `json:"usage"`
		} `json:"message"` // Anthropic message_start wraps usage in message
		Usage *proxyWireUsage `json:"usage"` // Anthropic message_delta / OpenAI chunks
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return // never let a malformed event affect forwarding or later events
	}
	if ev.Model != "" {
		o.model = ev.Model
	}
	if ev.Message != nil {
		if ev.Message.ID != "" {
			o.msgID = ev.Message.ID
		}
		if ev.Message.Model != "" {
			o.model = ev.Message.Model
		}
		if ev.Message.Usage != nil {
			o.merge(ev.Message.Usage)
		}
	}
	if ev.Usage != nil {
		o.merge(ev.Usage)
	}
}

// merge folds one usage payload into the accumulator.
//
// OpenAI dialect (prompt/completion fields set): converted exactly like the
// codex parser — input = prompt - cached (clamped), cache_read = cached — so
// the four components share Anthropic semantics store-wide (ADR-0004).
//
// Anthropic dialect: non-zero fields overwrite, so message_start seeds the
// input/cache components and the final message_delta's output_tokens replaces
// the placeholder output count from message_start.
func (o *usageObserver) merge(u *proxyWireUsage) {
	if u.PromptTokens > 0 || u.CompletionTokens > 0 || u.PromptTokensDetails != nil {
		var cached int64
		if u.PromptTokensDetails != nil {
			cached = min(u.PromptTokensDetails.CachedTokens, u.PromptTokens)
		}
		o.acc.input = u.PromptTokens - cached
		o.acc.cacheRead = cached
		o.acc.output = u.CompletionTokens
		return
	}
	if u.InputTokens > 0 {
		o.acc.input = u.InputTokens
	}
	if u.OutputTokens > 0 {
		o.acc.output = u.OutputTokens
	}
	if u.CacheReadInputTokens > 0 {
		o.acc.cacheRead = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		o.acc.cacheCreate = u.CacheCreationInputTokens
	}
}

// result returns the accumulated usage, the model, and the Anthropic message id
// when one was seen (ADR-0013).
func (o *usageObserver) result() (proxyTokens, string, string) {
	if !o.sse && o.raw.Len() > 0 {
		var v struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage *proxyWireUsage `json:"usage"`
		}
		if err := json.Unmarshal(o.raw.Bytes(), &v); err == nil {
			if v.ID != "" {
				o.msgID = v.ID
			}
			if v.Model != "" {
				o.model = v.Model
			}
			if v.Usage != nil {
				o.merge(v.Usage)
			}
		}
	}
	return o.acc, o.model, o.msgID
}

// ---- event delivery with retry ring ----

// proxyEmitter delivers events through the sink. Failed events stay in a ring
// (newest proxyRingCap kept) and are re-sent in front of the next batch; the
// server's idempotent ingest (event_id PK) makes any overlap harmless.
type proxyEmitter struct {
	mu   sync.Mutex
	buf  []model.Event
	sink func([]model.Event) error
}

func (e *proxyEmitter) emit(ev model.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buf = append(e.buf, ev)
	if len(e.buf) > proxyRingCap {
		e.buf = append(e.buf[:0], e.buf[len(e.buf)-proxyRingCap:]...)
	}
	batch := make([]model.Event, len(e.buf))
	copy(batch, e.buf)
	if err := e.sink(batch); err != nil {
		log.Printf("proxy: sink failed, %d event(s) buffered: %v", len(e.buf), err)
		return
	}
	e.buf = e.buf[:0]
}
