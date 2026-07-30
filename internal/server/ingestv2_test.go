package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

const (
	testV2DeviceA = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	testV2DeviceB = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a002"
	testV2BatchA  = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b001"
	testV2BatchB  = "018f2d5a-7b31-7d98-bf8e-3c2f35a1b002"
	testV2Boot    = "018f2d5a-7b31-7d98-bf8e-3c2f35a1c001"
)

func newIngestV2TestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ingest-v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.RegisterDevice(testV2DeviceA, "A", "device-a-token", []string{"events"}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterDevice(testV2DeviceB, "B", "device-b-token", []string{"events"}, 10); err != nil {
		t.Fatal(err)
	}
	return &Server{store: st, bcast: newBroadcaster()}, st
}

func validV2Envelope(batchID string) model.IngestEnvelopeV2 {
	return model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testV2DeviceA,
		BootID:          testV2Boot,
		BatchID:         batchID,
		Sequence:        42,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindEvents,
		Events: []model.Event{{
			EventID: "v2-event",
			TS:      1_785_319_948_062,
			Device:  testV2DeviceA,
			Source:  "codex",
		}},
	}
}

func validV2ProcEnvelope(batchID string, observedAt int64) model.IngestEnvelopeV2 {
	return model.IngestEnvelopeV2{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testV2DeviceA,
		BootID:          testV2Boot,
		BatchID:         batchID,
		Sequence:        42,
		CapturedAt:      1_785_319_948_062,
		Kind:            model.IngestKindProcs,
		Procs: &model.ProcReport{
			Device:     testV2DeviceA,
			ObservedAt: observedAt,
			Sessions: []model.ProcSession{{
				PID:       123,
				Source:    "codex",
				StartedAt: 1_785_319_900_000,
			}},
		},
	}
}

func ingestV2Request(t *testing.T, envelope model.IngestEnvelopeV2, token string) *http.Request {
	t.Helper()
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ingest", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func decodeV2Ack(t *testing.T, recorder *httptest.ResponseRecorder) model.IngestAckV2 {
	t.Helper()
	var ack model.IngestAckV2
	if err := json.Unmarshal(recorder.Body.Bytes(), &ack); err != nil {
		t.Fatalf("decode ack: %v; body=%q", err, recorder.Body.String())
	}
	return ack
}

func countV2Events(t *testing.T, st *store.Store) int64 {
	t.Helper()
	count, err := st.EventCount(store.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestHandleIngestV2AcceptsAndReplaysWithOriginalAck(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()
	envelope := validV2Envelope(testV2BatchA)

	first := httptest.NewRecorder()
	s.handleIngestV2(first, ingestV2Request(t, envelope, "device-a-token"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%q", first.Code, first.Body.String())
	}
	firstAck := decodeV2Ack(t, first)
	if firstAck.ProtocolVersion != model.IngestProtocolV2 ||
		firstAck.DeviceID != testV2DeviceA ||
		firstAck.BatchID != testV2BatchA ||
		firstAck.AckSequence != 42 ||
		firstAck.Accepted != 1 ||
		firstAck.Duplicates != 0 ||
		len(firstAck.Rejected) != 0 ||
		firstAck.ServerTime <= 0 {
		t.Fatalf("first ack = %#v", firstAck)
	}
	if countV2Events(t, st) != 1 {
		t.Fatal("accepted batch did not persist its event")
	}
	select {
	case <-notifications:
	default:
		t.Fatal("committed payload mutation did not notify SSE")
	}

	replay := httptest.NewRecorder()
	s.handleIngestV2(replay, ingestV2Request(t, envelope, "device-a-token"))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body=%q", replay.Code, replay.Body.String())
	}
	replayAck := decodeV2Ack(t, replay)
	if !reflect.DeepEqual(replayAck, firstAck) {
		t.Fatalf("replay ack = %#v, want original %#v", replayAck, firstAck)
	}
	if countV2Events(t, st) != 1 {
		t.Fatal("replay duplicated event mutation")
	}
	select {
	case <-notifications:
		t.Fatal("replayed receipt notified SSE")
	default:
	}
}

func TestHandleIngestV2AcknowledgesExistingEventAsDuplicateWithoutNotify(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	envelope := validV2Envelope(testV2BatchA)
	envelope.Events[0].SessionID = "known-session"
	if _, err := st.InsertEvents(envelope.Events, 100); err != nil {
		t.Fatal(err)
	}
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()

	recorder := httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, envelope, "device-a-token"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", recorder.Code, recorder.Body.String())
	}
	ack := decodeV2Ack(t, recorder)
	if ack.Accepted != 0 || ack.Duplicates != 1 {
		t.Fatalf("duplicate ack = %#v", ack)
	}
	select {
	case <-notifications:
		t.Fatal("receipt-only duplicate notified SSE")
	default:
	}
}

func TestHandleIngestV2RejectsNonPositiveProcessObservedAtWithoutReceipt(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	envelope := validV2ProcEnvelope(testV2BatchA, 0)

	recorder := httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, envelope, "device-a-token"))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%q", recorder.Code, recorder.Body.String())
	}
	ack := decodeV2Ack(t, recorder)
	if len(ack.Rejected) != 1 || ack.Rejected[0].Code != "invalid_proc_observed_at" {
		t.Fatalf("rejections = %#v", ack.Rejected)
	}
	reporters, err := st.ProcReporters(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 0 {
		t.Fatalf("invalid process report mutated state: %#v", reporters)
	}

	// Validation must happen before receipt persistence: the corrected payload
	// can reuse the same batch ID and be accepted.
	envelope.Procs.ObservedAt = 1_785_319_948_062
	retry := httptest.NewRecorder()
	s.handleIngestV2(retry, ingestV2Request(t, envelope, "device-a-token"))
	if retry.Code != http.StatusOK || decodeV2Ack(t, retry).Accepted != 1 {
		t.Fatalf("corrected retry status=%d body=%q", retry.Code, retry.Body.String())
	}
}

func TestHandleIngestV2IdenticalProcessSetRefreshDoesNotNotify(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()

	first := httptest.NewRecorder()
	s.handleIngestV2(first, ingestV2Request(t,
		validV2ProcEnvelope(testV2BatchA, 1_000), "device-a-token"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%q", first.Code, first.Body.String())
	}
	select {
	case <-notifications:
	default:
		t.Fatal("initial PID set did not notify")
	}

	secondEnvelope := validV2ProcEnvelope(testV2BatchB, 2_000)
	secondEnvelope.Sequence = 43
	second := httptest.NewRecorder()
	s.handleIngestV2(second, ingestV2Request(t, secondEnvelope, "device-a-token"))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body=%q", second.Code, second.Body.String())
	}
	select {
	case <-notifications:
		t.Fatal("identical PID set notified SSE")
	default:
	}
	running, err := st.RunningSessions(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].ObservedAt != 2_000 {
		t.Fatalf("process timestamp refresh did not commit: %#v", running)
	}
}

func TestHandleIngestV2ConcurrentSameBatchHasIdenticalAcksAndOneNotify(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()
	envelope := validV2Envelope(testV2BatchA)

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			recorder := httptest.NewRecorder()
			s.handleIngestV2(recorder, ingestV2Request(t, envelope, "device-a-token"))
			responses <- recorder
		}()
	}
	close(start)
	wg.Wait()
	close(responses)

	var acks []model.IngestAckV2
	for recorder := range responses {
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%q", recorder.Code, recorder.Body.String())
		}
		acks = append(acks, decodeV2Ack(t, recorder))
	}
	if len(acks) != 2 || !reflect.DeepEqual(acks[0], acks[1]) {
		t.Fatalf("concurrent acknowledgements = %#v, want identical values", acks)
	}
	if countV2Events(t, st) != 1 {
		t.Fatalf("event count = %d, want 1", countV2Events(t, st))
	}
	select {
	case <-notifications:
	default:
		t.Fatal("committed concurrent batch did not notify")
	}
	select {
	case <-notifications:
		t.Fatal("same concurrent batch notified more than once")
	default:
	}
}

func TestHandleIngestV2RejectsInvalidAndMixedDeviceWithoutMutation(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	envelope := validV2Envelope(testV2BatchA)
	envelope.Events[0].Device = testV2DeviceB

	recorder := httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, envelope, "device-a-token"))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%q", recorder.Code, recorder.Body.String())
	}
	ack := decodeV2Ack(t, recorder)
	if len(ack.Rejected) != 1 || ack.Rejected[0].Code != model.RejectionEventDeviceMismatch {
		t.Fatalf("rejections = %#v", ack.Rejected)
	}
	if ack.Accepted != 0 || ack.Duplicates != 0 || countV2Events(t, st) != 0 {
		t.Fatalf("invalid request mutated or acknowledged data: ack=%#v events=%d", ack, countV2Events(t, st))
	}

	// A rejected envelope must not reserve its batch ID as a receipt.
	envelope.Events[0].Device = testV2DeviceA
	retry := httptest.NewRecorder()
	s.handleIngestV2(retry, ingestV2Request(t, envelope, "device-a-token"))
	if retry.Code != http.StatusOK || decodeV2Ack(t, retry).Accepted != 1 {
		t.Fatalf("corrected retry status=%d body=%q", retry.Code, retry.Body.String())
	}
}

func TestHandleIngestV2ValidationAckDeclaresSupportedProtocol(t *testing.T) {
	s, _ := newIngestV2TestServer(t)
	envelope := validV2Envelope(testV2BatchA)
	envelope.ProtocolVersion = 1

	recorder := httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, envelope, "device-a-token"))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%q", recorder.Code, recorder.Body.String())
	}
	ack := decodeV2Ack(t, recorder)
	if ack.ProtocolVersion != model.IngestProtocolV2 {
		t.Fatalf("ack protocol_version = %d, want supported v2", ack.ProtocolVersion)
	}
	if len(ack.Rejected) != 1 || ack.Rejected[0].Code != model.RejectionProtocolVersion {
		t.Fatalf("rejections = %#v", ack.Rejected)
	}
}

func TestHandleIngestV2RejectsCrossDeviceBatchIDConflict(t *testing.T) {
	s, st := newIngestV2TestServer(t)
	first := validV2Envelope(testV2BatchA)
	recorder := httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, first, "device-a-token"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%q", recorder.Code, recorder.Body.String())
	}

	second := validV2Envelope(testV2BatchA)
	second.DeviceID = testV2DeviceB
	second.Events[0].Device = testV2DeviceB
	second.Events[0].EventID = "device-b-conflict"
	recorder = httptest.NewRecorder()
	s.handleIngestV2(recorder, ingestV2Request(t, second, "device-b-token"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409; body=%q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error != "batch_id_conflict" {
		t.Fatalf("conflict response = %q, decode err=%v", recorder.Body.String(), err)
	}
	if countV2Events(t, st) != 1 {
		t.Fatal("batch conflict mutated payload")
	}
}

func TestHandleIngestV2RejectsMalformedAndOversizedBodies(t *testing.T) {
	s, _ := newIngestV2TestServer(t)
	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "malformed JSON", body: `{"protocol_version":`, wantStatus: http.StatusBadRequest, wantCode: "malformed_request"},
		{name: "trailing JSON", body: `{}` + `{}`, wantStatus: http.StatusBadRequest, wantCode: "malformed_request"},
		{name: "oversized", body: strings.Repeat(" ", int(ingestV2BodyMax)+1), wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v2/ingest", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer device-a-token")
			recorder := httptest.NewRecorder()
			s.handleIngestV2(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("error response is not JSON: %v; body=%q", err, recorder.Body.String())
			}
			if response.Error != tc.wantCode {
				t.Fatalf("error = %q, want %q", response.Error, tc.wantCode)
			}
		})
	}
}

func TestHandleIngestV2RejectsUnauthorizedCredentialsWithoutMutation(t *testing.T) {
	for _, token := range []string{"", "wrong", "legacy-ingest", "read-token", "admin-token", "device-b-token"} {
		t.Run(token, func(t *testing.T) {
			s, st := newIngestV2TestServer(t)
			recorder := httptest.NewRecorder()
			s.handleIngestV2(recorder, ingestV2Request(t, validV2Envelope(testV2BatchA), token))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%q", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error != "unauthorized" {
				t.Fatalf("unauthorized response = %q, decode err=%v", recorder.Body.String(), err)
			}
			if countV2Events(t, st) != 0 {
				t.Fatal("unauthorized request mutated events")
			}
		})
	}
}
