// Package agent implements push-mode collection: scan local logs, durably
// enqueue v2 batches, and report them to the server in FIFO order. Legacy v1
// configurations retain their original synchronous upload behavior.
package agent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/proxy"
)

const (
	ingestAckBodyMax      int64 = 1 << 20
	relayV1IngestBodyMax  int64 = 64 << 20
	relayHeartbeatBodyMax int64 = 1 << 20
	relayTokenHeader            = "X-OmniToken-Relay-Token"
	heartbeatInterval           = 30 * time.Second
	outboxIdleInterval          = time.Second
)

type Config struct {
	ServerURL          string // e.g. https://hub.example or http://127.0.0.1:8788
	AllowInsecureHTTP  bool
	Token              string // legacy v1 bearer token
	ProtocolVersion    int    // zero and one are legacy v1; two uses the durable outbox
	DeviceID           string
	DeviceToken        string
	OutboxPath         string
	OutboxMaxBytes     int64
	AgentVersion       string
	Capabilities       []string
	DeviceName         string
	ClaudeDirs         []string
	CodexDirs          []string
	StatePath          string
	Interval           time.Duration
	RelayListen        string // e.g. ":8788"; empty = relay disabled
	RelayToken         string // protects this relay's listener
	RelayUpstreamToken string // next-hop relay credential; falls back to RelayToken
	// Since is the first instant to report; the zero time means no window.
	Since          time.Time
	ProxyListen    string            // e.g. "127.0.0.1:8899"; empty = proxy disabled
	ProxyUpstreams map[string]string // prefix -> upstream base
}

type enrollmentRequest struct {
	DeviceID     string   `json:"device_id"`
	DeviceToken  string   `json:"device_token"`
	DisplayName  string   `json:"display_name"`
	Capabilities []string `json:"capabilities"`
}

// Enroll registers a prepared stable identity using the independently scoped
// admin credential. Credentials are never included in returned errors.
func Enroll(serverURL, adminToken string, fc FileConfig, client *http.Client) error {
	if adminToken == "" {
		return errors.New("admin credential is required")
	}
	hubURL, err := validateServerURL(serverURL, fc.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	body, err := json.Marshal(enrollmentRequest{
		DeviceID:     fc.DeviceID,
		DeviceToken:  fc.DeviceToken,
		DisplayName:  fc.Name,
		Capabilities: []string{"events", "quotas", "procs", "heartbeat", "durable_outbox"},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", hubURL.String()+"/api/v2/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("enrollment request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enrollment rejected: %s", resp.Status)
	}
	return nil
}

type Agent struct {
	cfg    Config
	state  *collect.State
	client *http.Client
	probe  func() collect.ClaudeAuthProbe
	outbox *Outbox
	bootID string
	sleep  func(time.Duration)
	jitter func() float64
	now    func() time.Time

	heartbeatSequence atomic.Uint64
	lastScanAt        atomic.Int64
	lastUploadAt      atomic.Int64
}

func New(cfg Config) (*Agent, error) {
	if cfg.ProtocolVersion == 0 {
		cfg.ProtocolVersion = 1
	}
	if cfg.ProtocolVersion != 1 && cfg.ProtocolVersion != model.IngestProtocolV2 {
		return nil, fmt.Errorf("unsupported ingest protocol version %d", cfg.ProtocolVersion)
	}
	serverURL, err := validateServerURL(cfg.ServerURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	cfg.ServerURL = serverURL.String()
	if cfg.RelayListen != "" && cfg.RelayToken == "" {
		return nil, errors.New("relay token is required when relay is enabled")
	}
	st, err := collect.LoadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		cfg:    cfg,
		state:  st,
		client: &http.Client{Timeout: 60 * time.Second},
		probe:  collect.NewCachedProber(10 * time.Minute),
		sleep:  time.Sleep,
		jitter: mathrand.Float64,
		now:    time.Now,
	}
	if cfg.ProtocolVersion == model.IngestProtocolV2 {
		if cfg.DeviceID == "" || cfg.DeviceToken == "" {
			return nil, errors.New("protocol v2 requires device ID and device token")
		}
		agent.bootID, err = newUUID()
		if err != nil {
			return nil, fmt.Errorf("generate boot ID: %w", err)
		}
		path := cfg.OutboxPath
		if path == "" {
			path = filepath.Join(filepath.Dir(cfg.StatePath), "outbox.db")
		}
		agent.outbox, err = OpenOutbox(path, cfg.OutboxMaxBytes)
		if err != nil {
			return nil, err
		}
	}
	return agent, nil
}

func (a *Agent) Close() error {
	if a.outbox == nil {
		return nil
	}
	return a.outbox.Close()
}

// ResetOffsets makes the next pass re-read every log from the start; see
// collect.State.ResetOffsets. Same reason as the server's: the offsets are in
// memory once the agent is running, so this has to happen before the loop.
func (a *Agent) ResetOffsets() (int, error) { return a.state.ResetOffsets() }

// RunOnce performs a single scan+report pass (used for backfill and cron).
func (a *Agent) RunOnce() (int, error) {
	if a.isV2() {
		// A pre-scan drain lets a previously full outbox recover as soon as the
		// hub is reachable again. A failed attempt does not block collection
		// while durable capacity remains.
		_, _ = a.drainOutbox()
	}
	n, err := a.scanOnce()
	if err != nil || !a.isV2() {
		return n, err
	}
	_, err = a.drainOutbox()
	return n, err
}

func (a *Agent) scanOnce() (int, error) {
	specs := collect.LocalSpecs(a.cfg.ClaudeDirs, a.cfg.CodexDirs)
	sink := func(events []model.Event) error {
		collect.RefineProvider(events, a.probe) // local logs only (F9)
		return a.push(events)
	}
	// A push is a self-report, and on the server side that outranks an
	// observer's guess (ADR-0015) — which is exactly why the window matters
	// here. "An agent only reads its own machine's logs, so everything it finds
	// is this machine's work" is the obvious assumption and it is false whenever
	// a home directory is synced: the agent would then claim another machine's
	// history with full self-report authority. Config `since` bounds that.
	device := a.cfg.DeviceName
	if a.isV2() {
		device = a.cfg.DeviceID
	}
	n, err := collect.ScanSources(specs, device, a.state, collect.LocalRepoResolver, sink, a.pushQuotas, a.cfg.Since)
	if err == nil {
		a.lastScanAt.Store(a.currentTime().UnixMilli())
	}
	return n, err
}

// reportProcs sends this machine's running agent CLIs (ADR-0012).
//
// Kept out of RunOnce, which backfill and cron also call: process state belongs
// to the resident loop, not to a one-shot historical import. Legacy v1 keeps
// its latest-only behavior. V2 durably delivers every emitted report so an
// acknowledged sequence can never skip an unsent batch.
func (a *Agent) reportProcs() error {
	device := a.cfg.DeviceName
	if a.isV2() {
		device = a.cfg.DeviceID
	}
	report, err := collect.LiveProcesses(device, a.currentTime())
	if err != nil {
		return err
	}
	if a.isV2() {
		if err := a.enqueueEnvelope(model.IngestEnvelopeV2{
			Kind:  model.IngestKindProcs,
			Procs: &report,
		}); err != nil {
			return err
		}
		_, err = a.drainOutbox()
		return err
	}
	return a.postIngest(map[string]any{"procs": report})
}

func (a *Agent) Run() error {
	if a.cfg.RelayListen != "" {
		go a.runRelay()
	}
	if a.cfg.ProxyListen != "" {
		go func() {
			err := proxy.Run(proxy.Config{
				Listen:    a.cfg.ProxyListen,
				Device:    a.cfg.DeviceName,
				Upstreams: a.cfg.ProxyUpstreams,
			}, a.push)
			log.Printf("proxy: %v", err)
		}()
	}
	if a.isV2() {
		a.startUploadLoop()
		a.startHeartbeatLoop()
	}
	backoff := retryBackoff{base: time.Second, max: time.Minute, jitter: a.jitter}
	for {
		var n int
		var err error
		if a.isV2() {
			// Resident v2 collection only appends durable work. Upload and
			// heartbeat have independent workers, so a slow Hub cannot stall
			// filesystem scanning or make a healthy agent appear offline.
			n, err = a.scanOnce()
		} else {
			n, err = a.RunOnce()
		}
		if err != nil {
			log.Printf("agent: report failed (will retry): %v", err)
		} else if n > 0 {
			log.Printf("agent: reported %d events", n)
		}
		var statusErr error
		if !a.isV2() {
			statusErr = a.reportProcs()
		}
		if statusErr != nil {
			log.Printf("agent: status report failed: %v", statusErr)
		}
		if err != nil || statusErr != nil {
			a.sleep(backoff.Next())
			continue
		}
		backoff.Reset()
		a.sleep(a.cfg.Interval)
	}
}

func (a *Agent) startHeartbeatLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	go func() {
		defer ticker.Stop()
		a.runHeartbeatLoop(ticker.C)
	}()
}

// runHeartbeatLoop is deliberately independent of scanning and uploading.
// It reports immediately at startup and then at each heartbeat tick.
func (a *Agent) runHeartbeatLoop(ticks <-chan time.Time) {
	for {
		if err := a.sendHeartbeat(); err != nil {
			log.Printf("agent: heartbeat failed: %v", err)
		}
		if _, ok := <-ticks; !ok {
			return
		}
	}
}

func (a *Agent) startUploadLoop() {
	go func() {
		backoff := retryBackoff{base: time.Second, max: time.Minute, jitter: a.jitter}
		for {
			err := a.uploadOldest()
			switch {
			case errors.Is(err, ErrOutboxEmpty):
				backoff.Reset()
				time.Sleep(outboxIdleInterval)
			case err != nil:
				log.Printf("agent: upload failed (will retry): %v", err)
				time.Sleep(backoff.Next())
			default:
				backoff.Reset()
			}
		}
	}()
}

func (a *Agent) push(events []model.Event) error {
	if a.isV2() {
		return a.enqueueEvents(events)
	}
	return a.postIngest(map[string]any{"events": events})
}

// pushQuotas reports quota snapshots (ADR-0007) through the same endpoint.
func (a *Agent) pushQuotas(qs []model.QuotaSnapshot) error {
	if a.isV2() {
		return a.enqueueEnvelope(model.IngestEnvelopeV2{Kind: model.IngestKindQuotas, Quotas: qs})
	}
	return a.postIngest(map[string]any{"quotas": qs})
}

func (a *Agent) isV2() bool {
	return a.cfg.ProtocolVersion == model.IngestProtocolV2
}

func (a *Agent) currentTime() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *Agent) sendHeartbeat() error {
	stats, err := a.outbox.Stats()
	if err != nil {
		return err
	}
	now := a.currentTime()
	processState, err := collect.LiveProcesses(a.cfg.DeviceID, now)
	if err != nil {
		return err
	}
	capabilities := a.cfg.Capabilities
	if len(capabilities) == 0 {
		capabilities = []string{"events", "quotas", "procs", "heartbeat", "durable_outbox"}
	}
	heartbeat := model.Heartbeat{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        a.cfg.DeviceID,
		AgentVersion:    a.cfg.AgentVersion,
		BootID:          a.bootID,
		Sequence:        a.heartbeatSequence.Add(1),
		SentAt:          now.UnixMilli(),
		Capabilities:    append([]string(nil), capabilities...),
		QueuedBatches:   stats.QueuedBatches,
		QueuedBytes:     stats.QueuedBytes,
		OldestQueuedAt:  stats.OldestQueuedAt,
		LastScanAt:      a.lastScanAt.Load(),
		LastUploadAt:    a.lastUploadAt.Load(),
		ProcessState:    &processState,
	}
	body, err := json.Marshal(heartbeat)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", a.cfg.ServerURL+"/api/v2/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.DeviceToken)
	a.addRelayCredential(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("heartbeat server %s: %s", resp.Status, msg)
	}
	return nil
}

func (a *Agent) enqueueEnvelope(envelope model.IngestEnvelopeV2) error {
	prepared, err := a.prepareEnvelope(envelope)
	if err != nil {
		return err
	}
	_, err = a.outbox.EnqueueNext(prepared)
	return err
}

func (a *Agent) enqueueEvents(events []model.Event) error {
	envelopes, err := a.partitionEventEnvelopes(collapseRepeatedEventIDs(events))
	if err != nil {
		return err
	}
	_, err = a.outbox.EnqueueManyNext(envelopes)
	return err
}

// collapseRepeatedEventIDs keeps the first observation of each event_id, because
// an envelope carrying the same id twice is rejected whole
// (model.ValidateIngestEnvelope) and one scan legitimately produces such pairs.
//
// A single Claude Code generation is written as several JSONL lines — one per
// content block — and every one of them repeats the same message.id, requestId
// and usage, so the parser derives the same event_id for all of them by design
// (ADR-0004). On this developer's own logs that is 18,308 of 26,848 ids. The
// server's local collector never noticed: it inserts by event_id and the repeat
// is ignored there. The v2 agent refused the batch instead, and since a batch
// that never enqueues never advances an offset, that failure was not a dropped
// scan but a permanent one — the same lines were re-read and re-refused forever.
//
// Collapsing here rather than loosening the envelope keeps "one id, one row" a
// protocol invariant the Hub can rely on. First-wins matches the store's insert,
// so a log line reaching the Hub by two routes still yields one row either way.
func collapseRepeatedEventIDs(events []model.Event) []model.Event {
	seen := make(map[string]struct{}, len(events))
	collapsed := make([]model.Event, 0, len(events))
	for _, event := range events {
		if _, repeated := seen[event.EventID]; repeated {
			continue
		}
		seen[event.EventID] = struct{}{}
		collapsed = append(collapsed, event)
	}
	return collapsed
}

func (a *Agent) partitionEventEnvelopes(events []model.Event) ([]model.IngestEnvelopeV2, error) {
	prepared, err := a.prepareEnvelope(model.IngestEnvelopeV2{
		Kind:   model.IngestKindEvents,
		Events: events,
	})
	if err != nil {
		return nil, err
	}
	// Size using the longest possible decimal sequence. EnqueueManyNext will
	// assign a value no wider than this, so a chunk accepted here cannot grow
	// beyond the shared Hub limit when persisted.
	sizeProbe := prepared
	sizeProbe.Sequence = math.MaxUint64
	payload, err := json.Marshal(sizeProbe)
	if err != nil {
		return nil, fmt.Errorf("encode ingest envelope: %w", err)
	}
	if len(payload) <= model.MaxIngestEnvelopeBytes {
		return []model.IngestEnvelopeV2{prepared}, nil
	}
	if len(events) <= 1 {
		return nil, ErrEnvelopeTooLarge
	}
	middle := len(events) / 2
	left, err := a.partitionEventEnvelopes(events[:middle])
	if err != nil {
		return nil, err
	}
	right, err := a.partitionEventEnvelopes(events[middle:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func (a *Agent) prepareEnvelope(envelope model.IngestEnvelopeV2) (model.IngestEnvelopeV2, error) {
	batchID, err := newUUID()
	if err != nil {
		return model.IngestEnvelopeV2{}, fmt.Errorf("generate batch ID: %w", err)
	}
	envelope.ProtocolVersion = model.IngestProtocolV2
	envelope.DeviceID = a.cfg.DeviceID
	envelope.BootID = a.bootID
	envelope.BatchID = batchID
	envelope.Sequence = 0
	envelope.CapturedAt = a.currentTime().UnixMilli()
	return envelope, nil
}

func (a *Agent) drainOutbox() (int, error) {
	uploaded := 0
	for {
		err := a.uploadOldest()
		if errors.Is(err, ErrOutboxEmpty) {
			return uploaded, nil
		}
		if err != nil {
			return uploaded, err
		}
		uploaded++
	}
}

func (a *Agent) uploadOldest() error {
	envelope, err := a.outbox.PeekBatch()
	if err != nil {
		return err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", a.cfg.ServerURL+"/api/v2/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.DeviceToken)
	a.addRelayCredential(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("server %s: %s", resp.Status, msg)
	}
	ackBody, err := io.ReadAll(io.LimitReader(resp.Body, ingestAckBodyMax+1))
	if err != nil {
		return fmt.Errorf("read ingest acknowledgement: %w", err)
	}
	if int64(len(ackBody)) > ingestAckBodyMax {
		return errors.New("decode ingest acknowledgement: response too large")
	}
	var ack model.IngestAckV2
	decoder := json.NewDecoder(bytes.NewReader(ackBody))
	if err := decoder.Decode(&ack); err != nil {
		return fmt.Errorf("decode ingest acknowledgement: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("decode ingest acknowledgement: trailing JSON")
	}
	if err := a.outbox.Acknowledge(ack); err != nil {
		return err
	}
	a.lastUploadAt.Store(a.currentTime().UnixMilli())
	return nil
}

func newUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

type retryBackoff struct {
	base    time.Duration
	max     time.Duration
	attempt uint
	jitter  func() float64
}

func (b *retryBackoff) Next() time.Duration {
	cap := b.base
	for i := uint(0); i < b.attempt && cap < b.max; i++ {
		if cap > b.max/2 {
			cap = b.max
		} else {
			cap *= 2
		}
	}
	if cap > b.max {
		cap = b.max
	}
	b.attempt++
	return time.Duration(b.jitter() * float64(cap))
}

func (b *retryBackoff) Reset() {
	b.attempt = 0
}

func (a *Agent) postIngest(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", a.cfg.ServerURL+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	a.addRelayCredential(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("server %s: %s", resp.Status, msg)
	}
	return nil
}

// runRelay forwards ingest requests from peers that cannot reach the server
// directly (chainable: d → c → server). Stateless by design: a failed forward
// returns an error to the downstream agent, which keeps its offsets and
// retries later.
func (a *Agent) runRelay() {
	server, err := a.relayServer()
	if err != nil {
		log.Printf("relay: configure: %v", err)
		return
	}
	log.Printf("agent: relaying ingest on %s -> %s", a.cfg.RelayListen, a.cfg.ServerURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("relay: %v", err)
	}
}

func (a *Agent) relayServer() (*http.Server, error) {
	upstream, err := validateServerURL(a.cfg.ServerURL, a.cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	if a.cfg.RelayToken == "" {
		return nil, errors.New("relay token is required")
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	mux := http.NewServeMux()
	for path, maxBytes := range map[string]int64{
		"/api/v1/ingest":    relayV1IngestBodyMax,
		"/api/v2/ingest":    model.MaxIngestEnvelopeBytes,
		"/api/v2/heartbeat": relayHeartbeatBodyMax,
	} {
		mux.Handle("POST "+path, a.relayForward(maxBytes, proxy))
	}
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"relay"}`)
	})
	return &http.Server{
		Addr:              a.cfg.RelayListen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}, nil
}

func (a *Agent) relayForward(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(relayTokenHeader)
		if len(values) != 1 || !relayCredentialOK(values[0], a.cfg.RelayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Replace the accepted listener credential with the configured
		// next-hop credential. Authorization continues to carry the end
		// device's credential unchanged.
		r.Header.Set(relayTokenHeader, a.relayUpstreamToken())
		if r.ContentLength > maxBytes {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxBytes {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

func (a *Agent) addRelayCredential(req *http.Request) {
	if token := a.relayUpstreamToken(); token != "" {
		req.Header.Set(relayTokenHeader, token)
	}
}

func (a *Agent) relayUpstreamToken() string {
	if a.cfg.RelayUpstreamToken != "" {
		return a.cfg.RelayUpstreamToken
	}
	return a.cfg.RelayToken
}

func relayCredentialOK(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

func validateServerURL(raw string, allowInsecureHTTP bool) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("server URL scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("server URL host is required")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("server URL must not contain credentials, query, fragment, or base path")
	}
	if parsed.Scheme == "http" && !allowInsecureHTTP && !loopbackHost(parsed.Hostname()) {
		return nil, errors.New("plaintext HTTP requires a loopback host or allow_insecure_http")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
