package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	edgeagent "github.com/suool/omnitoken/internal/agent"
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

func TestEdgeHubV2OutageRestartReplayAndScopedAuthAcceptance(t *testing.T) {
	const (
		deviceToken = "device-a-acceptance-token"
		otherToken  = "device-b-acceptance-token"
		receivedAt  = int64(1_785_500_000_000)
	)
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	hubStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hubStore.RegisterDevice(testV2DeviceA, "agent-a", deviceToken, nil, receivedAt-1); err != nil {
		t.Fatal(err)
	}
	if _, err := hubStore.RegisterDevice(testV2DeviceB, "agent-b", otherToken, nil, receivedAt-1); err != nil {
		t.Fatal(err)
	}

	envelope := validV2Envelope("018f2d5a-7b31-7d98-bf8e-3c2f35a1b001")
	envelope.Events[0].EventID = "edge-acceptance-event"
	envelope.Events[0].InputTokens = 17
	outboxPath := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := edgeagent.OpenOutbox(outboxPath, edgeagent.DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(envelope); err != nil {
		t.Fatal(err)
	}

	// A real refused connection proves an unavailable Hub cannot remove the
	// oldest durable row.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailableURL := "http://" + listener.Addr().String() + "/api/v2/ingest"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := postAcceptanceEnvelope(unavailableURL, deviceToken, envelope); err == nil {
		t.Fatal("upload to stopped Hub unexpectedly succeeded")
	}
	if got, err := outbox.PeekBatch(); err != nil || got.BatchID != envelope.BatchID {
		t.Fatalf("outbox after outage = %#v, err=%v", got, err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}
	outbox, err = edgeagent.OpenOutbox(outboxPath, edgeagent.DefaultOutboxMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()

	hub := &Server{
		cfg:   &Config{Listen: "127.0.0.1:0"},
		store: hubStore,
		bcast: newBroadcaster(),
		now:   func() time.Time { return time.UnixMilli(receivedAt) },
	}
	httpHub := httptest.NewServer(hub.routes())
	ack, err := postAcceptanceEnvelope(httpHub.URL+"/api/v2/ingest", deviceToken, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Acknowledge(ack); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.PeekBatch(); !errors.Is(err, edgeagent.ErrOutboxEmpty) {
		t.Fatalf("acknowledged row retained: %v", err)
	}

	// Restart the authoritative Hub and replay the exact batch. The persisted
	// receipt must return the same acknowledgement and event_id dedup keeps a
	// single analytical row.
	httpHub.Close()
	if err := hubStore.Close(); err != nil {
		t.Fatal(err)
	}
	hubStore, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer hubStore.Close()
	hub = &Server{
		cfg:   &Config{Listen: "127.0.0.1:0"},
		store: hubStore,
		bcast: newBroadcaster(),
		now:   func() time.Time { return time.UnixMilli(receivedAt + 1_000) },
	}
	httpHub = httptest.NewServer(hub.routes())
	defer httpHub.Close()
	replayAck, err := postAcceptanceEnvelope(httpHub.URL+"/api/v2/ingest", deviceToken, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if replayAck.BatchID != ack.BatchID || replayAck.AckSequence != ack.AckSequence {
		t.Fatalf("replay ack = %#v, first ack = %#v", replayAck, ack)
	}
	totals, err := hubStore.Summary(time.UnixMilli(0), time.UnixMilli(receivedAt+10_000))
	if err != nil {
		t.Fatal(err)
	}
	if totals.Events != 1 || totals.InputTokens != 17 {
		t.Fatalf("replay totals = %#v, want one insertion", totals)
	}

	impersonated := envelope
	impersonated.BatchID = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b099"
	impersonated.Sequence++
	if _, status, err := postAcceptanceEnvelopeStatus(
		httpHub.URL+"/api/v2/ingest", otherToken, impersonated,
	); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("cross-device status=%d err=%v, want 401", status, err)
	}
	if err := hubStore.RevokeDevice(testV2DeviceA, receivedAt+2_000); err != nil {
		t.Fatal(err)
	}
	if _, status, err := postAcceptanceEnvelopeStatus(
		httpHub.URL+"/api/v2/ingest", deviceToken, impersonated,
	); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d err=%v, want 401", status, err)
	}
}

func postAcceptanceEnvelope(url, token string, envelope model.IngestEnvelopeV2) (model.IngestAckV2, error) {
	ack, status, err := postAcceptanceEnvelopeStatus(url, token, envelope)
	if err != nil {
		return model.IngestAckV2{}, err
	}
	if status != http.StatusOK {
		return model.IngestAckV2{}, errors.New("unexpected ingest status: " + http.StatusText(status))
	}
	return ack, nil
}

func postAcceptanceEnvelopeStatus(
	url, token string,
	envelope model.IngestEnvelopeV2,
) (model.IngestAckV2, int, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return model.IngestAckV2{}, 0, err
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return model.IngestAckV2{}, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return model.IngestAckV2{}, 0, err
	}
	defer response.Body.Close()
	var ack model.IngestAckV2
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
			return model.IngestAckV2{}, response.StatusCode, err
		}
	}
	return ack, response.StatusCode, nil
}
