package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

func newLiveTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	return &Server{cfg: &Config{}, store: st, prices: prices, bcast: newBroadcaster()}
}

// seedEventOn is seedEvent for tests that care which device an event came from.
func seedEventOn(t *testing.T, s *Server, id, device string, ts time.Time) {
	t.Helper()
	ev := model.Event{
		EventID: id, TS: ts.UnixMilli(), Device: device, Source: "claude-code",
		Model: "claude-sonnet-4-5", Provider: "anthropic",
		InputTokens: 100, OutputTokens: 50, SessionID: "sess-" + device,
	}
	if _, err := s.store.InsertEvents([]model.Event{ev}, ts.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

func seedEvent(t *testing.T, s *Server, id string, ts time.Time, input, output int64) {
	t.Helper()
	ev := model.Event{
		EventID: id, TS: ts.UnixMilli(), Device: "dev", Source: "claude-code",
		Model: "claude-sonnet-4-5", Provider: "anthropic",
		InputTokens: input, OutputTokens: output, SessionID: "sess",
	}
	if _, err := s.store.InsertEvents([]model.Event{ev}, ts.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

// The menubar client polls this instead of holding the SSE stream open, so it
// has to carry the burn rate the stream carries.
func TestHandleLiveReportsBurnOverTheWindow(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()

	// Two events inside the 10-minute burn window, one comfortably outside it.
	seedEvent(t, s, "in-1", now.Add(-1*time.Minute), 1000, 200)
	seedEvent(t, s, "in-2", now.Add(-5*time.Minute), 500, 100)
	seedEvent(t, s, "old", now.Add(-2*time.Hour), 999999, 999999)

	rec := httptest.NewRecorder()
	s.handleLive(rec, httptest.NewRequest(http.MethodGet, "/api/v1/live", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Burn struct {
			WindowMinutes int   `json:"window_minutes"`
			Tokens        int64 `json:"tokens"`
			OutputTokens  int64 `json:"output_tokens"`
			PerMinute     int64 `json:"per_minute"`
		} `json:"burn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 1000+200 + 500+100, with the two-hour-old event excluded.
	const wantTokens = 1800
	if got.Burn.Tokens != wantTokens {
		t.Errorf("burn.tokens = %d, want %d", got.Burn.Tokens, wantTokens)
	}
	if got.Burn.OutputTokens != 300 {
		t.Errorf("burn.output_tokens = %d, want 300", got.Burn.OutputTokens)
	}
	if got.Burn.WindowMinutes != 10 {
		t.Errorf("burn.window_minutes = %d, want 10", got.Burn.WindowMinutes)
	}
	if want := int64(wantTokens / 10); got.Burn.PerMinute != want {
		t.Errorf("burn.per_minute = %d, want %d", got.Burn.PerMinute, want)
	}
}

func TestLivePayloadUsesHeartbeatReceiptNotFutureClientEventForRegisteredLiveness(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	if _, err := s.store.RegisterDevice(testV2DeviceA, "Registered", "device-token", []string{"heartbeat"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TouchDevice(testV2DeviceA, now.Add(-20*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	seedEventOn(t, s, "future-clock", testV2DeviceA, now.Add(24*time.Hour))
	if _, err := s.store.RegisterDevice(testV2DeviceB, "No Usage", "device-token-b", nil, 1); err != nil {
		t.Fatal(err)
	}

	payload, err := s.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload["devices"])
	if err != nil {
		t.Fatal(err)
	}
	var devices []struct {
		Device          string `json:"device"`
		ConnectionState string `json:"connection_state"`
		IdentityStatus  string `json:"identity_status"`
	}
	if err := json.Unmarshal(raw, &devices); err != nil {
		t.Fatal(err)
	}
	byDevice := map[string]struct {
		State    string
		Identity string
	}{}
	for _, device := range devices {
		byDevice[device.Device] = struct {
			State    string
			Identity string
		}{device.ConnectionState, device.IdentityStatus}
	}
	if got := byDevice[testV2DeviceA]; got.State != "offline" || got.Identity != "registered" {
		t.Fatalf("future client event changed heartbeat liveness: %#v", got)
	}
	if got := byDevice[testV2DeviceB]; got.State != "offline" || got.Identity != "registered" {
		t.Fatalf("registered device without usage missing/offline state: %#v", got)
	}
}

// Panel and Live page must never disagree about the same ten minutes, which
// holds only as long as both render the payload livePayload builds. Compared
// by shape rather than deep equality: the two are built at different instants,
// so the wall-clock fields (window bounds, device active/idle) legitimately
// differ and would make a deep comparison flake.
func TestHandleLiveServesTheStreamSnapshotShape(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	seedEvent(t, s, "e1", now.Add(-1*time.Minute), 700, 300)

	rec := httptest.NewRecorder()
	s.handleLive(rec, httptest.NewRequest(http.MethodGet, "/api/v1/live", nil))

	var fromHandler map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fromHandler); err != nil {
		t.Fatalf("decode handler body: %v", err)
	}

	// What handleStream sends as its `snapshot` event.
	payload, err := s.livePayload(time.Now())
	if err != nil {
		t.Fatalf("livePayload: %v", err)
	}
	raw, _ := json.Marshal(payload)
	var fromStream map[string]any
	if err := json.Unmarshal(raw, &fromStream); err != nil {
		t.Fatalf("decode stream payload: %v", err)
	}

	for k := range fromStream {
		if _, ok := fromHandler[k]; !ok {
			t.Errorf("handler payload missing %q, which the stream snapshot carries", k)
		}
	}
	for k := range fromHandler {
		if _, ok := fromStream[k]; !ok {
			t.Errorf("handler payload has extra key %q the stream snapshot lacks", k)
		}
	}

	// Burn is the reason the endpoint exists, and it is window-relative rather
	// than instant-relative, so the two calls must agree on it exactly.
	gotBurn, _ := json.Marshal(fromHandler["burn"])
	wantBurn, _ := json.Marshal(fromStream["burn"])
	if string(gotBurn) != string(wantBurn) {
		t.Errorf("burn = %s, want %s", gotBurn, wantBurn)
	}
}

func TestLivePayloadExposesAdditiveContributionBreakdowns(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	events := []model.Event{
		{
			EventID: "speed-a", TS: now.Add(-10 * time.Second).UnixMilli(),
			Device: "mac", Source: "claude-code", Model: "anthropic.claude-opus-4-8",
			SessionID: "s1", OutputTokens: 1_000, GenMS: 10_000,
		},
		{
			EventID: "speed-b", TS: now.UnixMilli(),
			Device: "server", Source: "proxy", Model: "claude-opus-4-8",
			SessionID: "s2", OutputTokens: 1_000, GenMS: 10_000,
		},
	}
	if _, err := s.store.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	payload, err := s.livePayload(now)
	if err != nil {
		t.Fatalf("livePayload: %v", err)
	}
	raw, err := json.Marshal(payload["speed"])
	if err != nil {
		t.Fatal(err)
	}
	var speed store.LiveSpeed
	if err := json.Unmarshal(raw, &speed); err != nil {
		t.Fatalf("decode speed: %v", err)
	}
	if speed.TPS != 100 {
		t.Fatalf("aggregate tps = %v, want 100", speed.TPS)
	}
	for _, session := range speed.Sessions {
		if session.TPS != 100 || session.ContributionTPS != 50 {
			t.Errorf("session speed = %+v, want native=100 contribution=50", session)
		}
	}
	for name, rows := range map[string][]store.SpeedContribution{
		"sources": speed.Sources,
		"devices": speed.Devices,
		"models":  speed.Models,
	} {
		var sum float64
		for _, row := range rows {
			sum += row.ContributionTPS
		}
		if math.Abs(sum-speed.TPS) > 1e-9 {
			t.Errorf("%s contribution sum = %v, aggregate = %v", name, sum, speed.TPS)
		}
	}
}

// The whole point of ADR-0012: a device with an agent reporting an empty
// process list is idle, while a device with no agent at all (SSH-pulled) has no
// process data. The payload must let the panel tell those apart, or it will
// render "nothing open" for a machine it simply cannot see.
func TestLivePayloadSeparatesIdleFromUnobserved(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	seedEventOn(t, s, "e-mac", "mac", now.Add(-1*time.Minute))
	seedEventOn(t, s, "e-ssh", "ssh-box", now.Add(-1*time.Minute))

	// mac runs an agent and currently has nothing open; ssh-box reports nothing.
	if _, err := s.store.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: now.UnixMilli(),
	}); err != nil {
		t.Fatalf("ApplyProcReport: %v", err)
	}

	payload, err := s.livePayload(now)
	if err != nil {
		t.Fatalf("livePayload: %v", err)
	}
	raw, _ := json.Marshal(payload)
	var got struct {
		Devices []struct {
			Device   string `json:"device"`
			HasProcs bool   `json:"has_procs"`
			Running  int    `json:"running"`
		} `json:"devices"`
		Processes struct {
			Sessions  []store.RunningSession `json:"sessions"`
			Reporters []store.ProcReporter   `json:"reporters"`
		} `json:"processes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]bool{}
	for _, d := range got.Devices {
		byName[d.Device] = d.HasProcs
		if d.Running != 0 {
			t.Errorf("%s running = %d, want 0", d.Device, d.Running)
		}
	}
	if !byName["mac"] {
		t.Error("mac has an agent reporting an empty list, but has_procs is false")
	}
	if byName["ssh-box"] {
		t.Error("ssh-box has no agent, but has_procs is true — that reads as 'nothing open'")
	}
	if len(got.Processes.Reporters) != 1 || got.Processes.Reporters[0].Device != "mac" {
		t.Errorf("reporters = %+v, want only mac", got.Processes.Reporters)
	}
}

// A running process is reported whether or not it has produced tokens lately —
// that is the difference from the events-inferred session list beside it.
func TestLivePayloadReportsIdleButOpenSession(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	// Last token an hour ago: outside every event-based window.
	seedEventOn(t, s, "old", "mac", now.Add(-time.Hour))
	if _, err := s.store.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: now.UnixMilli(),
		Sessions: []model.ProcSession{
			{PID: 4242, Source: "claude-code", StartedAt: now.Add(-2 * time.Hour).UnixMilli()},
		},
	}); err != nil {
		t.Fatalf("ApplyProcReport: %v", err)
	}

	payload, err := s.livePayload(now)
	if err != nil {
		t.Fatalf("livePayload: %v", err)
	}
	raw, _ := json.Marshal(payload)
	var got struct {
		Sessions  []store.LiveSession `json:"sessions"`
		Processes struct {
			Sessions []store.RunningSession `json:"sessions"`
		} `json:"processes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Sessions) != 0 {
		t.Fatalf("event-inferred sessions = %+v, want none an hour after the last token", got.Sessions)
	}
	if len(got.Processes.Sessions) != 1 || got.Processes.Sessions[0].PID != 4242 {
		t.Fatalf("processes.sessions = %+v, want pid 4242 still open", got.Processes.Sessions)
	}
}
