package server

import (
	"encoding/json"
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
