package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// ADR-0020. A Codex fork copies the parent thread's history into a new rollout
// verbatim, changing only the line timestamps — so one generation reaches the
// store twice under two different event_ids. dedup_key is the second uniqueness
// constraint that catches it; these tests are the guarantee that it does, and
// that it never merges anything else.

const forkKey = "cxg:ae967f7e6dbd30de6fbcfea0"

// live is the generation as its own rollout recorded it.
func live(ts int64) model.Event {
	return model.Event{
		EventID: "cx:aaaaaaaaaaaaaaaaaaaaaaaa", DedupKey: forkKey, TS: ts,
		Device: "mac", Source: "codex", Model: "gpt-5.6-sol",
		InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 20,
		CacheCreationTokens: 5, GenMS: 45500, TTFTMS: 5000,
	}
}

// copied is the same generation as a fork replayed it: same usage, same dedup
// key, a new event_id, and the fork's flush instant for a timestamp. gen_ms is
// zero because ADR-0009 already declines to measure a replayed turn.
func copied(ts int64) model.Event {
	e := live(ts)
	e.EventID = "cx:bbbbbbbbbbbbbbbbbbbbbbbb"
	e.GenMS, e.TTFTMS = 0, 0
	return e
}

type totals struct {
	rows                        int
	input, output, read, create int64
}

func sum(t *testing.T, s *Store) totals {
	t.Helper()
	var got totals
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0) FROM events`).
		Scan(&got.rows, &got.input, &got.output, &got.read, &got.create)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func survivor(t *testing.T, s *Store) (id string, ts int64) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT event_id, ts FROM events WHERE dedup_key = ?`, forkKey).Scan(&id, &ts); err != nil {
		t.Fatal(err)
	}
	return id, ts
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dedup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The whole point: one generation, two rollout files, counted once. And the row
// that survives is the one whose timestamp is real — a fork stamps its copy with
// the flush instant, which on real data sat eleven days away from the truth.
func TestForkedCopyCountedOnce(t *testing.T) {
	const t1, t2 = int64(1785034850000), int64(1785900000000)
	for _, tc := range []struct {
		name  string
		order []model.Event
	}{
		{"original first", []model.Event{live(t1), copied(t2)}},
		{"copy first", []model.Event{copied(t2), live(t1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			now := time.Now().UnixMilli()
			for _, e := range tc.order {
				if _, err := s.InsertEvents([]model.Event{e}, now); err != nil {
					t.Fatal(err)
				}
			}
			got := sum(t, s)
			want := totals{rows: 1, input: 1000, output: 100, read: 20, create: 5}
			if got != want {
				t.Fatalf("counts = %+v, want %+v (the copy must not add tokens)", got, want)
			}
			id, ts := survivor(t, s)
			if ts != t1 || id != live(t1).EventID {
				t.Errorf("survivor = %s @ %d, want the original %s @ %d — the copy's timestamp is the fork's flush instant, not when the work happened",
					id, ts, live(t1).EventID, t1)
			}
		})
	}
}

// The 845 copies already in the database predate the key. A rescan re-reads both
// rollouts, backfills the key onto the row that has none, and the second one
// then collides — which is how the historical duplicates get cleaned up without
// rebuilding anything (ADR-0020 §3.4).
func TestRescanBackfillsKeyAndRemovesHistoricalDuplicate(t *testing.T) {
	const t1, t2 = int64(1785034850000), int64(1785900000000)
	s := open(t)
	now := time.Now().UnixMilli()

	// History as written by the parser before ADR-0020: no keys, both rows kept.
	before := []model.Event{live(t1), copied(t2)}
	for i := range before {
		before[i].DedupKey = ""
	}
	if n, err := s.InsertEvents(before, now); err != nil || n != 2 {
		t.Fatalf("seeding history: n=%d err=%v", n, err)
	}
	if got := sum(t, s); got.rows != 2 || got.output != 200 {
		t.Fatalf("seed = %+v, want the duplicate that ADR-0020 is about", got)
	}

	// The rescan: same event_ids, now carrying keys.
	if _, err := s.InsertEvents([]model.Event{live(t1), copied(t2)}, now); err != nil {
		t.Fatal(err)
	}
	got := sum(t, s)
	want := totals{rows: 1, input: 1000, output: 100, read: 20, create: 5}
	if got != want {
		t.Fatalf("after rescan = %+v, want %+v", got, want)
	}
	if id, ts := survivor(t, s); id != live(t1).EventID || ts != t1 {
		t.Errorf("survivor = %s @ %d, want %s @ %d", id, ts, live(t1).EventID, t1)
	}

	// A second rescan must be a no-op: the cleanup happens exactly once.
	if _, err := s.InsertEvents([]model.Event{live(t1), copied(t2)}, now); err != nil {
		t.Fatal(err)
	}
	if again := sum(t, s); again != want {
		t.Errorf("second rescan changed the database: %+v, want %+v", again, want)
	}
}

// An empty key means "no second opinion", never "same as the other empty one".
// 341 local events have no key at all (old Codex, MCP counter turn ids), and
// every source other than Codex leaves it empty entirely.
func TestEmptyDedupKeysNeverMerge(t *testing.T) {
	s := open(t)
	var events []model.Event
	for i, id := range []string{"cc:msg_1:req_1", "cc:msg_2:req_2", "cx:no_key_at_all"} {
		events = append(events, model.Event{
			EventID: id, TS: int64(1785034850000 + i), Device: "mac",
			Source: "claude-code", OutputTokens: 10,
		})
	}
	n, err := s.InsertEvents(events, time.Now().UnixMilli())
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v — rows without a key must all be kept", n, err)
	}
	if got := sum(t, s); got.rows != 3 || got.output != 30 {
		t.Errorf("counts = %+v, want 3 rows / 30 output", got)
	}
}

// Same generation, same key, same event_id — the ordinary re-scan / double
// channel case ADR-0004 already covered. Adding a key must not disturb it, and
// in particular must not delete the row and re-insert it.
func TestSameEventTwiceIsStillIdempotent(t *testing.T) {
	s := open(t)
	now := time.Now().UnixMilli()
	if n, err := s.InsertEvents([]model.Event{live(1785034850000)}, now); err != nil || n != 1 {
		t.Fatalf("first: n=%d err=%v", n, err)
	}
	n, err := s.InsertEvents([]model.Event{live(1785034850000)}, now)
	if err != nil || n != 0 {
		t.Fatalf("repeat: n=%d err=%v, want 0 inserted", n, err)
	}
	want := totals{rows: 1, input: 1000, output: 100, read: 20, create: 5}
	if got := sum(t, s); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

// Two genuinely different generations must never be merged by the key, however
// similar. This is the failure mode that matters most: an undercount is
// invisible, unlike the overcount being fixed.
func TestDistinctKeysBothSurvive(t *testing.T) {
	s := open(t)
	a := live(1785034850000)
	b := live(1785034860000)
	b.EventID = "cx:cccccccccccccccccccccccc"
	b.DedupKey = "cxg:5fbe85d7f16f2193dc199915"
	n, err := s.InsertEvents([]model.Event{a, b}, time.Now().UnixMilli())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := sum(t, s); got.rows != 2 || got.output != 200 {
		t.Errorf("counts = %+v, want both generations kept", got)
	}
}

// The database, not the caller, is what guarantees this: no code path may write
// two rows sharing a key, so a future producer that forgets the resolution
// cannot reintroduce the duplicate.
func TestDedupKeyUniquenessIsEnforcedBySchema(t *testing.T) {
	s := open(t)
	if _, err := s.db.Exec(
		`INSERT INTO events (event_id, ts, dedup_key) VALUES ('a', 1, ?), ('b', 2, ?)`,
		forkKey, forkKey); err == nil {
		t.Fatal("the schema must reject two rows with the same dedup_key")
	}
	if _, err := s.db.Exec(
		`INSERT INTO events (event_id, ts, dedup_key) VALUES ('c', 1, ''), ('d', 2, '')`); err != nil {
		t.Fatalf("empty keys must stay exempt from the constraint: %v", err)
	}
}
