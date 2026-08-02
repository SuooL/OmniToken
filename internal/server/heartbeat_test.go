package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

const heartbeatReceivedAt int64 = 1_785_400_000_000

func newHeartbeatTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "heartbeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{
		cfg:   &Config{AdminToken: "admin-secret"},
		store: st,
		bcast: newBroadcaster(),
		now:   func() time.Time { return time.UnixMilli(heartbeatReceivedAt) },
	}, st
}

func jsonRequest(t *testing.T, method, path string, payload any, token string) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestEnrollV2RequiresAdminAndRenamePreservesIdentity(t *testing.T) {
	s, st := newHeartbeatTestServer(t)
	request := enrollmentV2Request{
		DeviceID:     testV2DeviceA,
		DeviceToken:  "device-a-token",
		DisplayName:  "Before",
		Capabilities: []string{"events", "heartbeat"},
	}

	unauthorized := httptest.NewRecorder()
	s.adminAuth(s.handleEnrollV2)(unauthorized, jsonRequest(t, http.MethodPost, "/api/v2/enroll", request, "wrong"))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	first := httptest.NewRecorder()
	s.adminAuth(s.handleEnrollV2)(first, jsonRequest(t, http.MethodPost, "/api/v2/enroll", request, "admin-secret"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%q", first.Code, first.Body.String())
	}

	request.DisplayName = "After"
	rename := httptest.NewRecorder()
	s.adminAuth(s.handleEnrollV2)(rename, jsonRequest(t, http.MethodPost, "/api/v2/enroll", request, "admin-secret"))
	if rename.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%q", rename.Code, rename.Body.String())
	}
	record, err := st.DeviceByID(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if record.DeviceID != testV2DeviceA || record.DisplayName != "After" {
		t.Fatalf("renamed record = %#v", record)
	}
	if _, ok, err := st.AuthenticateDevice(testV2DeviceA, "device-a-token"); err != nil || !ok {
		t.Fatalf("rename replaced credential: ok=%v err=%v", ok, err)
	}
}

func TestEnrollV2RefusesWhenServerHasNoAdminCredential(t *testing.T) {
	s, _ := newHeartbeatTestServer(t)
	s.cfg.AdminToken = ""
	recorder := httptest.NewRecorder()
	s.adminAuth(s.handleEnrollV2)(recorder, jsonRequest(t, http.MethodPost, "/api/v2/enroll", enrollmentV2Request{
		DeviceID:    testV2DeviceA,
		DeviceToken: "device-a-token",
		DisplayName: "Agent",
	}, ""))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%q, want 401", recorder.Code, recorder.Body.String())
	}
}

func validHeartbeat() model.Heartbeat {
	return model.Heartbeat{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        testV2DeviceA,
		AgentVersion:    "test-version",
		BootID:          testV2Boot,
		Sequence:        9,
		SentAt:          heartbeatReceivedAt + int64(24*time.Hour/time.Millisecond),
		Capabilities:    []string{"events", "quotas", "heartbeat"},
		QueuedBatches:   2,
		QueuedBytes:     300,
		OldestQueuedAt:  heartbeatReceivedAt - 1_000,
		LastScanAt:      heartbeatReceivedAt - 500,
		LastUploadAt:    heartbeatReceivedAt - 250,
		ProcessState: &model.ProcReport{
			Device:     testV2DeviceA,
			ObservedAt: heartbeatReceivedAt + int64(time.Hour/time.Millisecond),
			Sessions:   []model.ProcSession{{PID: 42, Source: "codex"}},
		},
	}
}

func TestHeartbeatUsesDeviceAuthAndServerReceiveTime(t *testing.T) {
	s, st := newHeartbeatTestServer(t)
	if _, err := st.RegisterDevice(testV2DeviceA, "Agent", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	heartbeat := validHeartbeat()
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()
	recorder := httptest.NewRecorder()
	s.handleHeartbeatV2(recorder, jsonRequest(t, http.MethodPost, "/api/v2/heartbeat", heartbeat, "device-a-token"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	record, err := st.DeviceByID(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if record.LastSeenAt != heartbeatReceivedAt {
		t.Fatalf("last_seen_at=%d, want server time %d", record.LastSeenAt, heartbeatReceivedAt)
	}
	saved, receivedAt, err := st.LatestHeartbeat(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SentAt != heartbeat.SentAt || saved.QueuedBatches != 2 || receivedAt != heartbeatReceivedAt {
		t.Fatalf("saved heartbeat=%#v received_at=%d", saved, receivedAt)
	}
	reporters, err := st.ProcReporters(time.UnixMilli(heartbeatReceivedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 1 || reporters[0].ObservedAt != heartbeatReceivedAt {
		t.Fatalf("process report did not use server time: %#v", reporters)
	}
	select {
	case <-notifications:
	default:
		t.Fatal("successful heartbeat did not notify live subscribers")
	}
}

func TestHeartbeatRejectsNonDeviceCredentialsWithoutTouchingLiveness(t *testing.T) {
	s, st := newHeartbeatTestServer(t)
	if _, err := st.RegisterDevice(testV2DeviceA, "Agent", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	notifications, unsubscribe := s.bcast.Subscribe()
	defer unsubscribe()
	for _, token := range []string{"", "admin-secret", "wrong-device-token"} {
		recorder := httptest.NewRecorder()
		s.handleHeartbeatV2(recorder, jsonRequest(t, http.MethodPost, "/api/v2/heartbeat", validHeartbeat(), token))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status=%d, want 401", token, recorder.Code)
		}
	}
	record, err := st.DeviceByID(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if record.LastSeenAt != 0 {
		t.Fatalf("unauthorized heartbeat touched last_seen_at=%d", record.LastSeenAt)
	}
	select {
	case <-notifications:
		t.Fatal("unauthorized heartbeat notified live subscribers")
	default:
	}
}

func TestHeartbeatBodyIsBoundedStrictAndIdentityBound(t *testing.T) {
	s, st := newHeartbeatTestServer(t)
	if _, err := st.RegisterDevice(testV2DeviceA, "Agent", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	validBody, err := json.Marshal(validHeartbeat())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "unknown field", body: strings.TrimSuffix(string(validBody), "}") + `,"unknown":true}`, code: http.StatusBadRequest},
		{name: "multiple values", body: string(validBody) + `{}`, code: http.StatusBadRequest},
		{name: "oversized", body: `{"padding":"` + strings.Repeat("x", int(heartbeatV2BodyMax)) + `"}`, code: http.StatusRequestEntityTooLarge},
		{name: "wrong protocol", body: strings.Replace(string(validBody), `"protocol_version":2`, `"protocol_version":1`, 1), code: http.StatusBadRequest},
		{name: "process identity mismatch", body: strings.Replace(string(validBody), `"device":"`+testV2DeviceA+`"`, `"device":"`+testV2DeviceB+`"`, 1), code: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v2/heartbeat", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer device-a-token")
			recorder := httptest.NewRecorder()
			s.handleHeartbeatV2(recorder, req)
			if recorder.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%q", recorder.Code, tc.code, recorder.Body.String())
			}
		})
	}
}
