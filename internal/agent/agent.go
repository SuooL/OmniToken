// Package agent implements push-mode collection: scan local logs, durably
// enqueue v2 batches, and report them to the server in FIFO order. Legacy v1
// configurations retain their original synchronous upload behavior.
package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
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

type Config struct {
	ServerURL       string // e.g. http://192.0.2.1:8787 or http://peer:8788 (relay)
	Token           string // legacy v1 bearer token
	ProtocolVersion int    // zero and one are legacy v1; two uses the durable outbox
	DeviceID        string
	DeviceToken     string
	OutboxPath      string
	OutboxMaxBytes  int64
	AgentVersion    string
	Capabilities    []string
	DeviceName      string
	ClaudeDirs      []string
	CodexDirs       []string
	StatePath       string
	Interval        time.Duration
	RelayListen     string // e.g. ":8788"; empty = relay disabled
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
	req, err := http.NewRequest("POST", strings.TrimSuffix(serverURL, "/")+"/api/v2/enroll", bytes.NewReader(body))
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
	probe  func() string
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
	if err != nil || !a.isV2() {
		return n, err
	}
	_, err = a.drainOutbox()
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
	backoff := retryBackoff{base: time.Second, max: time.Minute, jitter: a.jitter}
	for {
		n, err := a.RunOnce()
		if err != nil {
			log.Printf("agent: report failed (will retry): %v", err)
		} else if n > 0 {
			log.Printf("agent: reported %d events", n)
		}
		var statusErr error
		if a.isV2() {
			statusErr = a.sendHeartbeat()
		} else {
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

func (a *Agent) push(events []model.Event) error {
	if a.isV2() {
		return a.enqueueEnvelope(model.IngestEnvelopeV2{Kind: model.IngestKindEvents, Events: events})
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
	sequence, err := a.outbox.NextSequence()
	if err != nil {
		return err
	}
	batchID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate batch ID: %w", err)
	}
	envelope.ProtocolVersion = model.IngestProtocolV2
	envelope.DeviceID = a.cfg.DeviceID
	envelope.BootID = a.bootID
	envelope.BatchID = batchID
	envelope.Sequence = sequence
	envelope.CapturedAt = a.currentTime().UnixMilli()
	return a.outbox.Enqueue(envelope)
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
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("server %s: %s", resp.Status, msg)
	}
	var ack model.IngestAckV2
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
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
	upstream, err := url.Parse(a.cfg.ServerURL)
	if err != nil {
		log.Printf("relay: bad server url: %v", err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/ingest", proxy)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"relay"}`))
	})
	log.Printf("agent: relaying ingest on %s -> %s", a.cfg.RelayListen, a.cfg.ServerURL)
	if err := http.ListenAndServe(a.cfg.RelayListen, mux); err != nil {
		log.Printf("relay: %v", err)
	}
}
