package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func speedStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// gen builds one event whose generation ran [ts-genMS, ts].
func gen(id, sess string, ts time.Time, genMS, out int64) model.Event {
	return model.Event{
		EventID: id, TS: ts.UnixMilli(), Device: "mac", Source: "claude-code",
		Model: "claude-opus-4-8", SessionID: sess, OutputTokens: out, GenMS: genMS,
	}
}

func TestLiveSpeedDividesByUnionNotSum(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	// Two sessions generating over the *same* ten seconds: 1000 tokens each.
	// Summing durations would give 20s and halve the answer; the wall clock
	// only advanced 10s, so the machine did 2000 tokens in 10s.
	evs := []model.Event{
		gen("a", "s1", now.Add(-5*time.Second), 10_000, 1000),
		gen("b", "s2", now.Add(-5*time.Second), 10_000, 1000),
	}
	if _, err := st.InsertEvents(evs, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if got.ActiveMS != 10_000 {
		t.Errorf("machine active_ms = %d, want 10000 (union, not 20000)", got.ActiveMS)
	}
	if got.TPS != 200 {
		t.Errorf("machine tps = %v, want 200", got.TPS)
	}
	if len(got.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(got.Sessions))
	}
	// Each stream individually is still doing 100 tok/s.
	for _, s := range got.Sessions {
		if s.TPS != 100 {
			t.Errorf("session %s tps = %v, want 100", s.SessionID, s.TPS)
		}
	}
}

// Subagents share the parent's session_id, so one session really can hold
// overlapping streams. Within a session the duration must be a union too.
func TestLiveSpeedUnionsOverlapWithinOneSession(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	evs := []model.Event{
		gen("a", "s1", now.Add(-4*time.Second), 6_000, 600),
		gen("b", "s1", now.Add(-2*time.Second), 6_000, 600),
	}
	if _, err := st.InsertEvents(evs, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(got.Sessions))
	}
	// [-10s,-4s] ∪ [-8s,-2s] = [-10s,-2s] = 8s, not 12s.
	if got.Sessions[0].ActiveMS != 8_000 {
		t.Errorf("active_ms = %d, want 8000", got.Sessions[0].ActiveMS)
	}
	if got.Sessions[0].TPS != 150 {
		t.Errorf("tps = %v, want 150", got.Sessions[0].TPS)
	}
}

// A response straddling the window start must only lend the window the part
// that falls inside it, or active time exceeds elapsed time.
func TestLiveSpeedClipsToWindow(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	// Generation ran for 60s but ended 10s into a 30s window.
	evs := []model.Event{gen("a", "s1", now.Add(-20*time.Second), 60_000, 1000)}
	if _, err := st.InsertEvents(evs, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.LiveSpeedSince(now.Add(-30*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if got.ActiveMS != 10_000 {
		t.Errorf("active_ms = %d, want 10000 (clipped to the window)", got.ActiveMS)
	}
	if got.ActiveMS > int64(got.WindowSeconds)*1000 {
		t.Errorf("active_ms %d exceeds the %ds window", got.ActiveMS, got.WindowSeconds)
	}
}

func TestLiveSpeedFiltersByDeviceAndSkipsUnmeasured(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	other := gen("b", "s2", now.Add(-2*time.Second), 4_000, 400)
	other.Device = "server"
	// gen_ms 0 means "not measured" (pre-ADR-0009 rows), never "instant".
	unmeasured := gen("c", "s3", now.Add(-2*time.Second), 0, 900)

	if _, err := st.InsertEvents([]model.Event{
		gen("a", "s1", now.Add(-2*time.Second), 4_000, 400), other, unmeasured,
	}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "mac")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s1" {
		t.Fatalf("sessions = %+v, want only s1 on mac", got.Sessions)
	}
	if got.OutputTokens != 400 {
		t.Errorf("output_tokens = %d, want 400", got.OutputTokens)
	}
}

func TestLiveSpeedEmptyWindow(t *testing.T) {
	st := speedStore(t)
	now := time.Now()
	got, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if got.TPS != 0 || got.ActiveMS != 0 || len(got.Sessions) != 0 {
		t.Errorf("empty window gave %+v", got)
	}
}

// The backfill path must fill gen_ms in without ever re-counting an event.
func TestInsertEventsBackfillsGenMSWithoutDoubleCounting(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	// Arrives first without a generation interval, as pre-ADR-0009 rows did.
	old := gen("a", "s1", now.Add(-2*time.Second), 0, 500)
	n, err := st.InsertEvents([]model.Event{old}, now.UnixMilli())
	if err != nil || n != 1 {
		t.Fatalf("first insert: n=%d err=%v", n, err)
	}

	// Same event re-observed by a rescan, now carrying gen_ms.
	filled := gen("a", "s1", now.Add(-2*time.Second), 5_000, 500)
	n, err = st.InsertEvents([]model.Event{filled}, now.UnixMilli())
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if n != 0 {
		t.Errorf("rescan reported %d inserted, want 0 — dedup must still hold", n)
	}

	got, err := st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if err != nil {
		t.Fatalf("LiveSpeedSince: %v", err)
	}
	if got.OutputTokens != 500 {
		t.Errorf("output_tokens = %d, want 500 — tokens must never be counted twice", got.OutputTokens)
	}
	if got.ActiveMS != 5_000 {
		t.Errorf("active_ms = %d, want 5000 — gen_ms should have been backfilled", got.ActiveMS)
	}

	// A later observation without gen_ms must not erase what we have.
	if _, err := st.InsertEvents([]model.Event{old}, now.UnixMilli()); err != nil {
		t.Fatalf("third insert: %v", err)
	}
	got, _ = st.LiveSpeedSince(now.Add(-60*time.Second), now, "")
	if got.ActiveMS != 5_000 {
		t.Errorf("active_ms = %d after a gen_ms-less re-observation, want 5000", got.ActiveMS)
	}
}

// Burn must describe new tokens, not the same context re-read every turn:
// cache_read was 99.6% of the total on real traffic and made the rate
// meaningless (ADR-0009 follow-up; matches abtop's token_rate).
func TestTokensSinceExcludesCacheRead(t *testing.T) {
	st := speedStore(t)
	now := time.Now()

	ev := gen("a", "s1", now.Add(-1*time.Minute), 5_000, 300)
	ev.InputTokens = 100
	ev.CacheCreationTokens = 200
	ev.CacheReadTokens = 900_000 // dwarfs everything else, as it does in practice
	if _, err := st.InsertEvents([]model.Event{ev}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	total, output, err := st.TokensSince(now.Add(-10 * time.Minute))
	if err != nil {
		t.Fatalf("TokensSince: %v", err)
	}
	if want := int64(100 + 300 + 200); total != want {
		t.Errorf("total = %d, want %d (input+output+cache_creation, no cache_read)", total, want)
	}
	if output != 300 {
		t.Errorf("output = %d, want 300", output)
	}
}
