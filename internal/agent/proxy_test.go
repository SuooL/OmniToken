package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// eventTrap is a sink that records every batch it receives; failN first calls
// return an error to exercise the retry ring.
type eventTrap struct {
	mu    sync.Mutex
	calls [][]model.Event
	failN int
}

func (t *eventTrap) sink(evs []model.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	batch := make([]model.Event, len(evs))
	copy(batch, evs)
	t.calls = append(t.calls, batch)
	if len(t.calls) <= t.failN {
		return errors.New("sink down")
	}
	return nil
}

func (t *eventTrap) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

func (t *eventTrap) call(i int) []model.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls[i]
}

// waitCalls waits for the async emitter to reach n sink calls.
func waitCalls(t *testing.T, trap *eventTrap, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for trap.callCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d sink calls (got %d)", n, trap.callCount())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestProxy(t *testing.T, upstreams map[string]string, sink func([]model.Event) error) *httptest.Server {
	t.Helper()
	px := httptest.NewServer(newProxyHandler(ProxyConfig{Device: "test-dev", Upstreams: upstreams}, sink))
	t.Cleanup(px.Close)
	return px
}

func TestProxyAnthropicNonStream(t *testing.T) {
	const upstreamResp = `{"id":"msg_01","type":"message","model":"claude-sonnet-4-20250514",` +
		`"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}`
	var gotPath, gotBody string
	var gotHeader http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		time.Sleep(10 * time.Millisecond) // make TTFT/Duration measurable in ms
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstreamResp)
	}))
	defer up.Close()

	trap := &eventTrap{}
	px := newTestProxy(t, map[string]string{"anthropic": up.URL}, trap.sink)

	reqBody := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", px.URL+"/anthropic/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	clientBody, _ := io.ReadAll(resp.Body)

	// Forwarding must be transparent: path stripped of prefix, headers and
	// body verbatim, response returned untouched.
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", gotPath)
	}
	if gotBody != reqBody {
		t.Fatalf("upstream body = %q, want %q", gotBody, reqBody)
	}
	for _, h := range []string{"x-api-key", "anthropic-version", "Content-Type"} {
		if gotHeader.Get(h) != req.Header.Get(h) {
			t.Fatalf("upstream header %s = %q, want %q", h, gotHeader.Get(h), req.Header.Get(h))
		}
	}
	if resp.StatusCode != http.StatusOK || string(clientBody) != upstreamResp {
		t.Fatalf("client got %d %q, want 200 %q", resp.StatusCode, clientBody, upstreamResp)
	}

	waitCalls(t, trap, 1)
	evs := trap.call(0)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.InputTokens != 100 || ev.OutputTokens != 50 || ev.CacheReadTokens != 30 || ev.CacheCreationTokens != 20 {
		t.Fatalf("tokens = in %d out %d cr %d cc %d, want 100/50/30/20",
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheCreationTokens)
	}
	if ev.TTFTMS <= 0 || ev.DurationMS <= 0 || ev.TTFTMS > ev.DurationMS {
		t.Fatalf("timings: ttft %dms duration %dms, want 0 < ttft <= duration", ev.TTFTMS, ev.DurationMS)
	}
	if ev.Source != ProxySource || ev.Provider != "anthropic-api" || ev.Device != "test-dev" {
		t.Fatalf("source/provider/device = %q/%q/%q", ev.Source, ev.Provider, ev.Device)
	}
	if ev.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("model = %q", ev.Model)
	}
	sum := sha256.Sum256([]byte("sk-ant-test-key"))
	if want := hex.EncodeToString(sum[:])[:12]; ev.AccountLabel != want {
		t.Fatalf("account label = %q, want %q (and never the raw key)", ev.AccountLabel, want)
	}
	if !strings.HasPrefix(ev.EventID, "px:") || len(ev.EventID) != len("px:")+24 {
		t.Fatalf("event id = %q, want px:<24 hex>", ev.EventID)
	}
	if ev.CWD != "" {
		t.Fatalf("cwd = %q, want empty", ev.CWD)
	}
}

func TestProxyAnthropicSSEStream(t *testing.T) {
	chunks := []string{
		"event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"msg_02","model":"claude-sonnet-4-20250514",` +
			`"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}` + "\n\n",
		"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n",
		"event: message_delta\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":512}}` + "\n\n",
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(10 * time.Millisecond) // TTFT
		for _, c := range chunks {
			io.WriteString(w, c)
			fl.Flush()
			time.Sleep(25 * time.Millisecond) // slow generation → Duration >> TTFT
		}
	}))
	defer up.Close()

	trap := &eventTrap{}
	px := newTestProxy(t, map[string]string{"anthropic": up.URL}, trap.sink)

	req, _ := http.NewRequest("POST", px.URL+"/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-20250514","stream":true,"messages":[]}`))
	req.Header.Set("x-api-key", "sk-ant-test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	clientBody, _ := io.ReadAll(resp.Body)

	if want := strings.Join(chunks, ""); string(clientBody) != want {
		t.Fatalf("client stream = %q, want full untouched SSE stream %q", clientBody, want)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	waitCalls(t, trap, 1)
	ev := trap.call(0)[0]
	// input/cache from message_start; output must come from the final
	// message_delta (512), not message_start's placeholder (1).
	if ev.InputTokens != 25 || ev.OutputTokens != 512 || ev.CacheReadTokens != 10 || ev.CacheCreationTokens != 5 {
		t.Fatalf("tokens = in %d out %d cr %d cc %d, want 25/512/10/5",
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheCreationTokens)
	}
	if ev.TTFTMS <= 0 || ev.TTFTMS >= ev.DurationMS {
		t.Fatalf("timings: ttft %dms duration %dms, want 0 < ttft < duration", ev.TTFTMS, ev.DurationMS)
	}
}

func TestProxyOpenAIStreamWithUsage(t *testing.T) {
	chunks := []string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06",` +
			`"choices":[{"index":0,"delta":{"content":"Hi"}}],"usage":null}` + "\n\n",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":9,"total_tokens":109,"prompt_tokens_details":{"cached_tokens":40}}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(5 * time.Millisecond)
		for _, c := range chunks {
			io.WriteString(w, c)
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer up.Close()

	trap := &eventTrap{}
	px := newTestProxy(t, map[string]string{"openai": up.URL}, trap.sink)

	req, _ := http.NewRequest("POST", px.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-openai-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	clientBody, _ := io.ReadAll(resp.Body)
	if want := strings.Join(chunks, ""); string(clientBody) != want {
		t.Fatalf("client stream = %q, want %q", clientBody, want)
	}

	waitCalls(t, trap, 1)
	ev := trap.call(0)[0]
	// OpenAI semantics converted like the codex parser: prompt_tokens includes
	// cached → input = 100-40, cache_read = 40.
	if ev.InputTokens != 60 || ev.CacheReadTokens != 40 || ev.OutputTokens != 9 || ev.CacheCreationTokens != 0 {
		t.Fatalf("tokens = in %d cr %d out %d cc %d, want 60/40/9/0",
			ev.InputTokens, ev.CacheReadTokens, ev.OutputTokens, ev.CacheCreationTokens)
	}
	if ev.Provider != "openai-api" {
		t.Fatalf("provider = %q, want openai-api", ev.Provider)
	}
	if ev.Model != "gpt-4o" { // request body model wins; stream chunks only refine when absent
		t.Fatalf("model = %q, want gpt-4o", ev.Model)
	}
}

func TestProxySinkFailureBuffersAndRetries(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"claude-sonnet-4","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()

	trap := &eventTrap{failN: 1} // first delivery fails, later ones succeed
	px := newTestProxy(t, map[string]string{"anthropic": up.URL}, trap.sink)

	post := func() {
		t.Helper()
		resp, err := http.Post(px.URL+"/anthropic/v1/messages", "application/json",
			strings.NewReader(`{"model":"claude-sonnet-4"}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("proxy returned %d; a sink failure must not affect forwarding", resp.StatusCode)
		}
	}

	post() // event 1 → sink fails → buffered
	waitCalls(t, trap, 1)
	if got := trap.call(0); len(got) != 1 {
		t.Fatalf("first sink call had %d events, want 1", len(got))
	}

	post() // event 2 → buffered event 1 is re-sent in the same batch
	waitCalls(t, trap, 2)
	second := trap.call(1)
	if len(second) != 2 {
		t.Fatalf("second sink call had %d events, want 2 (buffered + new)", len(second))
	}
	if second[0].EventID != trap.call(0)[0].EventID {
		t.Fatalf("buffered event not re-sent first: %q vs %q", second[0].EventID, trap.call(0)[0].EventID)
	}
	if second[0].EventID == second[1].EventID {
		t.Fatal("event ids must be unique per request")
	}

	post() // buffer was cleared after success → batch of 1 again
	waitCalls(t, trap, 3)
	if got := trap.call(2); len(got) != 1 {
		t.Fatalf("third sink call had %d events, want 1 (buffer cleared)", len(got))
	}
}

func TestProxyUnknownPrefixAndBadBase(t *testing.T) {
	trap := &eventTrap{}
	px := newTestProxy(t, map[string]string{"broken": "::not-a-url"}, trap.sink)

	resp, err := http.Post(px.URL+"/nope/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "unknown upstream prefix") {
		t.Fatalf("unknown prefix: got %d %q, want 404 JSON error", resp.StatusCode, body)
	}

	resp, err = http.Post(px.URL+"/broken/v1/x", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "bad upstream base") {
		t.Fatalf("bad base: got %d %q, want 502 JSON error", resp.StatusCode, body)
	}
	if trap.callCount() != 0 {
		t.Fatalf("error responses must not emit events, got %d sink calls", trap.callCount())
	}
}
