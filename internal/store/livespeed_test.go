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

// The proxy and the log see one request from two sides (ADR-0013). Merging them
// must add what each knows and change nothing that was already there — above
// all, not a single count.
func TestMergeSecondObservationFillsWithoutDoubleCounting(t *testing.T) {
	s, err := Open(t.TempDir() + "/merge.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id = "cc:msg_01ABC:req_011CxyZ"
	ts := time.Now().UnixMilli()

	// The proxy reports first — it fires the moment the request ends, while the
	// collector is still waiting for its next tick.
	proxyEv := model.Event{
		EventID: id, TS: ts, Device: "mac", Source: "proxy",
		Model: "claude-opus-4-8", Provider: "anthropic-oauth",
		InputTokens: 100, OutputTokens: 250, CacheReadTokens: 40,
		GenMS: 4200, TTFTMS: 310, AccountLabel: "abc123",
	}
	n, err := s.InsertEvents([]model.Event{proxyEv}, ts)
	if err != nil || n != 1 {
		t.Fatalf("proxy insert: n=%d err=%v", n, err)
	}

	// The log describes the same request: same tokens, plus everything the
	// proxy could not know.
	logEv := model.Event{
		EventID: id, TS: ts, Device: "mac", Source: "claude-code",
		Model: "claude-opus-4-8", Provider: "anthropic",
		InputTokens: 100, OutputTokens: 250, CacheReadTokens: 40,
		DurationMS: 99000, // ADR-0006 gap semantics — must not land on this row
		GenMS:      7777,  // a log-derived estimate; the measured one wins
		SessionID:  "sess-1", CWD: "/src/omnitoken", Repo: "local:OmniToken",
		GitBranch: "dev", AppVersion: "2.1.121",
	}
	n, err = s.InsertEvents([]model.Event{logEv}, ts)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("inserted = %d, want 0 — the second observation must not create a row", n)
	}

	var (
		count, input, output, cacheRead, genMS, ttft, duration int64
		source, sessionID, repo, branch, cwd, appVersion       string
	)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 — one request is one row", count)
	}
	if err := s.db.QueryRow(
		`SELECT input_tokens, output_tokens, cache_read_tokens, gen_ms, ttft_ms, duration_ms,
		        source, session_id, repo, git_branch, cwd, app_version FROM events`).
		Scan(&input, &output, &cacheRead, &genMS, &ttft, &duration,
			&source, &sessionID, &repo, &branch, &cwd, &appVersion); err != nil {
		t.Fatal(err)
	}

	// Counts: untouched, and counted once.
	if input != 100 || output != 250 || cacheRead != 40 {
		t.Errorf("tokens = %d/%d/%d, want 100/250/40 — merging must never move a count",
			input, output, cacheRead)
	}
	// The proxy's measurement stands; the log's estimate does not overwrite it.
	if genMS != 4200 || ttft != 310 {
		t.Errorf("gen_ms/ttft_ms = %d/%d, want 4200/310", genMS, ttft)
	}
	// duration_ms carries ADR-0006's meaning, so only the log may set it — and
	// it must be able to, or every proxied request would drop out of F8 work
	// time. The proxy's own measured span never lands here.
	if duration != 99000 {
		t.Errorf("duration_ms = %d, want the log's 99000 gap", duration)
	}
	// Attribution only the log knows.
	if sessionID != "sess-1" || repo != "local:OmniToken" || branch != "dev" ||
		cwd != "/src/omnitoken" || appVersion != "2.1.121" {
		t.Errorf("attribution not filled in: sess=%q repo=%q branch=%q cwd=%q ver=%q",
			sessionID, repo, branch, cwd, appVersion)
	}
	// The tool owns the source, not the way we happened to see it.
	if source != "claude-code" {
		t.Errorf("source = %q, want claude-code", source)
	}

	// Re-observing either side again changes nothing at all.
	for _, ev := range []model.Event{proxyEv, logEv} {
		if n, err := s.InsertEvents([]model.Event{ev}, ts); err != nil || n != 0 {
			t.Fatalf("re-observation: n=%d err=%v", n, err)
		}
	}
	var input2, genMS2 int64
	var source2 string
	if err := s.db.QueryRow(`SELECT input_tokens, gen_ms, source FROM events`).
		Scan(&input2, &genMS2, &source2); err != nil {
		t.Fatal(err)
	}
	if input2 != input || genMS2 != genMS || source2 != source {
		t.Errorf("a repeat observation changed the row: %d/%d/%q", input2, genMS2, source2)
	}
}

// The promotion is one-directional: a proxy observation arriving after the log
// must not relabel the row as proxy-sourced.
func TestProxyObservationDoesNotStealSource(t *testing.T) {
	s, err := Open(t.TempDir() + "/promote.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id = "cc:msg_02:req_02"
	ts := time.Now().UnixMilli()
	if _, err := s.InsertEvents([]model.Event{{
		EventID: id, TS: ts, Device: "mac", Source: "claude-code",
		Model: "claude-opus-4-8", OutputTokens: 10, Repo: "local:x",
	}}, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEvents([]model.Event{{
		EventID: id, TS: ts, Device: "mac", Source: "proxy",
		Model: "claude-opus-4-8", OutputTokens: 10, TTFTMS: 250,
	}}, ts); err != nil {
		t.Fatal(err)
	}
	var source string
	var ttft int64
	if err := s.db.QueryRow(`SELECT source, ttft_ms FROM events`).Scan(&source, &ttft); err != nil {
		t.Fatal(err)
	}
	if source != "claude-code" {
		t.Errorf("source = %q, want claude-code", source)
	}
	if ttft != 250 {
		t.Errorf("ttft_ms = %d, want 250 — the proxy still contributes its measurement", ttft)
	}
}

// The proxy must never set duration_ms on a row a log owns: that column is F8's
// gap, not a generation time.
func TestProxyNeverWritesDurationOnSharedRow(t *testing.T) {
	s, err := Open(t.TempDir() + "/dur.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id = "cc:msg_03:req_03"
	ts := time.Now().UnixMilli()
	if _, err := s.InsertEvents([]model.Event{{
		EventID: id, TS: ts, Source: "claude-code", Model: "claude-opus-4-8",
		OutputTokens: 10, DurationMS: 45000,
	}}, ts); err != nil {
		t.Fatal(err)
	}
	// A proxy observation carrying a measured span in duration_ms (as a
	// standalone row would) must not disturb the stored gap.
	if _, err := s.InsertEvents([]model.Event{{
		EventID: id, TS: ts, Source: "proxy", Model: "claude-opus-4-8",
		OutputTokens: 10, DurationMS: 3000, GenMS: 3000, TTFTMS: 200,
	}}, ts); err != nil {
		t.Fatal(err)
	}
	var duration, genMS, ttft int64
	if err := s.db.QueryRow(`SELECT duration_ms, gen_ms, ttft_ms FROM events`).
		Scan(&duration, &genMS, &ttft); err != nil {
		t.Fatal(err)
	}
	if duration != 45000 {
		t.Errorf("duration_ms = %d, want the log's 45000 untouched", duration)
	}
	if genMS != 3000 || ttft != 200 {
		t.Errorf("gen_ms/ttft_ms = %d/%d, want the proxy's 3000/200", genMS, ttft)
	}
}
