package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func v2TestAgent(t *testing.T, handler http.HandlerFunc) *Agent {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	return &Agent{
		cfg: Config{
			ServerURL:       server.URL,
			ProtocolVersion: model.IngestProtocolV2,
			DeviceID:        outboxDeviceID,
			DeviceToken:     "device-secret",
		},
		client: server.Client(),
		outbox: outbox,
	}
}

func writeAck(t *testing.T, w http.ResponseWriter, envelope model.IngestEnvelopeV2) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ackFor(envelope)); err != nil {
		t.Fatal(err)
	}
}

func TestUploadOldestDeletesOnlyAfterExactAcknowledgement(t *testing.T) {
	var requestPath, authorization string
	agent := v2TestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authorization = r.Header.Get("Authorization")
		var envelope model.IngestEnvelopeV2
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		writeAck(t, w, envelope)
	})
	envelope := outboxEnvelope(outboxBatchA, 7)
	if err := agent.outbox.Enqueue(envelope); err != nil {
		t.Fatal(err)
	}

	if err := agent.uploadOldest(); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/api/v2/ingest" || authorization != "Bearer device-secret" {
		t.Fatalf("request path=%q authorization=%q", requestPath, authorization)
	}
	if _, err := agent.outbox.PeekBatch(); !errors.Is(err, ErrOutboxEmpty) {
		t.Fatalf("batch retained after exact ack: %v", err)
	}
}

func TestV2PushSucceedsAfterDurableEnqueueWithoutNetwork(t *testing.T) {
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	agent := &Agent{
		cfg: Config{
			ServerURL:       "http://127.0.0.1:1",
			ProtocolVersion: model.IngestProtocolV2,
			DeviceID:        outboxDeviceID,
			DeviceToken:     "device-secret",
		},
		outbox: outbox,
		bootID: outboxBootID,
	}
	event := model.Event{
		EventID: "durably-enqueued-event",
		TS:      time.Now().UnixMilli(),
		Device:  outboxDeviceID,
	}
	if err := agent.push([]model.Event{event}); err != nil {
		t.Fatalf("scanner sink failed before upload: %v", err)
	}
	got, err := outbox.PeekBatch()
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != outboxDeviceID || got.BatchID == "" || got.Sequence != 1 ||
		len(got.Events) != 1 || got.Events[0].EventID != event.EventID {
		t.Fatalf("durable envelope = %#v", got)
	}
}

func TestDefaultProtocolRetainsLegacyV1Endpoint(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	agent := &Agent{
		cfg:    Config{ServerURL: server.URL, Token: "legacy-token"},
		client: server.Client(),
	}
	if err := agent.push([]model.Event{{EventID: "legacy"}}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/ingest" {
		t.Fatalf("default protocol path = %q, want /api/v1/ingest", path)
	}
}

func TestUploadOldestRetainsBatchForEveryUntrustedResponse(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "non-2xx", handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "retry", http.StatusServiceUnavailable) }},
		{name: "malformed ack", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"protocol_version":`)) }},
		{name: "nonmatching ack", handler: func(w http.ResponseWriter, _ *http.Request) {
			ack := ackFor(outboxEnvelope(outboxBatchA, 9))
			ack.AckSequence++
			_ = json.NewEncoder(w).Encode(ack)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := v2TestAgent(t, tc.handler)
			envelope := outboxEnvelope(outboxBatchA, 9)
			if err := agent.outbox.Enqueue(envelope); err != nil {
				t.Fatal(err)
			}
			if err := agent.uploadOldest(); err == nil {
				t.Fatal("untrusted response accepted")
			}
			got, err := agent.outbox.PeekBatch()
			if err != nil || got.BatchID != envelope.BatchID {
				t.Fatalf("oldest after failure = %#v, err=%v", got, err)
			}
		})
	}
}

func TestUploadRetrySendsStableEnvelope(t *testing.T) {
	var bodies [][]byte
	attempt := 0
	agent := v2TestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		bodies = append(bodies, append([]byte(nil), body.Bytes()...))
		attempt++
		if attempt == 1 {
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}
		var envelope model.IngestEnvelopeV2
		if err := json.Unmarshal(body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		writeAck(t, w, envelope)
	})
	if err := agent.outbox.Enqueue(outboxEnvelope(outboxBatchA, 17)); err != nil {
		t.Fatal(err)
	}
	if err := agent.uploadOldest(); err == nil {
		t.Fatal("first upload unexpectedly succeeded")
	}
	if err := agent.uploadOldest(); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry changed durable envelope:\nfirst=%s\nsecond=%s", bodies[0], bodies[1])
	}
}

func TestDrainOutboxStopsAfterPartialSuccess(t *testing.T) {
	requests := 0
	agent := v2TestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope model.IngestEnvelopeV2
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if requests == 2 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		writeAck(t, w, envelope)
	})
	first := outboxEnvelope(outboxBatchA, 1)
	second := outboxEnvelope(outboxBatchB, 2)
	if err := agent.outbox.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	if err := agent.outbox.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	uploaded, err := agent.drainOutbox()
	if err == nil || uploaded != 1 {
		t.Fatalf("drain uploaded=%d err=%v, want one success then failure", uploaded, err)
	}
	got, err := agent.outbox.PeekBatch()
	if err != nil || got.BatchID != second.BatchID {
		t.Fatalf("oldest after partial drain = %#v, err=%v", got, err)
	}
}

func TestRetryBackoffIsExponentialJitterAndResets(t *testing.T) {
	backoff := retryBackoff{
		base:   time.Second,
		max:    8 * time.Second,
		jitter: func() float64 { return 0.5 },
	}
	for i, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		if got := backoff.Next(); got != want {
			t.Fatalf("attempt %d delay=%v, want %v", i+1, got, want)
		}
	}
	backoff.Reset()
	if got := backoff.Next(); got != 500*time.Millisecond {
		t.Fatalf("delay after reset=%v, want 500ms", got)
	}
}

func TestSendHeartbeatIncludesIdentityCapabilitiesProcessAndOutboxStats(t *testing.T) {
	var received model.Heartbeat
	var authorization, path string
	agent := v2TestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"received_at": 123})
	})
	agent.cfg.AgentVersion = "test-version"
	agent.cfg.Capabilities = []string{"events", "heartbeat"}
	agent.bootID = outboxBootID
	agent.now = func() time.Time { return time.UnixMilli(1_785_400_000_000) }
	if err := agent.outbox.Enqueue(outboxEnvelope(outboxBatchA, 1)); err != nil {
		t.Fatal(err)
	}

	if err := agent.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v2/heartbeat" || authorization != "Bearer device-secret" {
		t.Fatalf("path=%q authorization=%q", path, authorization)
	}
	if received.ProtocolVersion != 2 || received.DeviceID != outboxDeviceID ||
		received.BootID != outboxBootID || received.AgentVersion != "test-version" ||
		received.Sequence != 1 || received.SentAt != 1_785_400_000_000 ||
		received.QueuedBatches != 1 || received.QueuedBytes <= 0 ||
		received.ProcessState == nil || received.ProcessState.Device != outboxDeviceID {
		t.Fatalf("heartbeat = %#v", received)
	}
	if !reflect.DeepEqual(received.Capabilities, []string{"events", "heartbeat"}) {
		t.Fatalf("capabilities = %v", received.Capabilities)
	}
	if err := agent.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if received.Sequence != 2 {
		t.Fatalf("second heartbeat sequence=%d, want 2", received.Sequence)
	}
}

func TestRunOnceDoesNotSendResidentHeartbeat(t *testing.T) {
	heartbeatRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/heartbeat" {
			heartbeatRequests++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	temp := t.TempDir()
	agent, err := New(Config{
		ServerURL:       server.URL,
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        outboxDeviceID,
		DeviceToken:     "device-secret",
		ClaudeDirs:      []string{filepath.Join(temp, "claude")},
		CodexDirs:       []string{filepath.Join(temp, "codex")},
		StatePath:       filepath.Join(temp, "state.json"),
		OutboxPath:      filepath.Join(temp, "outbox.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := agent.RunOnce(); err != nil {
		t.Fatal(err)
	}
	if heartbeatRequests != 0 {
		t.Fatalf("RunOnce sent %d resident heartbeats", heartbeatRequests)
	}
}
