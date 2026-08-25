package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// codexEvent builds a fully-populated codex-shaped event: dedup_key, gen_ms,
// ttft_ms and every text column set, i.e. a row that has nothing left to fill
// once stored. This is the shape a resumed/rescanned Codex rollout re-delivers
// on every growth (FullReparse), so it is the batch the ingest fast path must
// process without redundant per-row UPDATE probes (ADR-0030).
func codexEvent(id string, ts time.Time) model.Event {
	return model.Event{
		EventID:      id,
		DedupKey:     "cxg:" + id,
		TS:           ts.UnixMilli(),
		Device:       "mac",
		Source:       "codex",
		Model:        "gpt-5.6-sol",
		Provider:     "custom",
		InputTokens:  100,
		OutputTokens: 42,
		GenMS:        5000,
		TTFTMS:       800,
		SessionID:    "sess-" + id,
		CWD:          "/work",
		Repo:         "github.com/x/y",
		AppVersion:   "0.149",
	}
}

// TestReingestIdenticalCodexBatchIsNoOp pins the invariant the fast path must
// preserve: re-applying a batch of already-stored, fully-populated events
// changes nothing — no new rows, no fills, no mutation — and every column keeps
// its stored value. A fast path that wrongly mutated (or dropped) a self-owned
// dedup_key row would fail here.
func TestReingestIdenticalCodexBatchIsNoOp(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	batch := make([]model.Event, 50)
	for i := range batch {
		batch[i] = codexEvent(string(rune('a'+i%26))+string(rune('0'+i/26)), now.Add(-time.Duration(i)*time.Second))
	}

	first, err := s.InsertEventsFrom(batch, now.UnixMilli(), OriginSelf)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first != len(batch) {
		t.Fatalf("first insert stored %d, want %d", first, len(batch))
	}

	// Snapshot every mutable column before the second application.
	type row struct {
		gen, ttft            int64
		sess, cwd, repo, ver string
		src, origin, prov    string
		dedup                string
	}
	before := map[string]row{}
	for _, e := range batch {
		var r row
		if err := s.rdb.QueryRow(`SELECT gen_ms, ttft_ms, session_id, cwd, repo, app_version, source, device_origin, provider, dedup_key FROM events WHERE event_id = ?`, e.EventID).
			Scan(&r.gen, &r.ttft, &r.sess, &r.cwd, &r.repo, &r.ver, &r.src, &r.origin, &r.prov, &r.dedup); err != nil {
			t.Fatalf("snapshot %s: %v", e.EventID, err)
		}
		before[e.EventID] = r
	}

	second, err := s.InsertEventsFrom(batch, now.UnixMilli()+1000, OriginSelf)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if second != 0 {
		t.Fatalf("re-insert stored %d rows, want 0 (all duplicates)", second)
	}

	var total int
	if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != len(batch) {
		t.Fatalf("row count %d after re-ingest, want %d (no rows added or dropped)", total, len(batch))
	}
	for _, e := range batch {
		var r row
		if err := s.rdb.QueryRow(`SELECT gen_ms, ttft_ms, session_id, cwd, repo, app_version, source, device_origin, provider, dedup_key FROM events WHERE event_id = ?`, e.EventID).
			Scan(&r.gen, &r.ttft, &r.sess, &r.cwd, &r.repo, &r.ver, &r.src, &r.origin, &r.prov, &r.dedup); err != nil {
			t.Fatalf("reread %s: %v", e.EventID, err)
		}
		if r != before[e.EventID] {
			t.Fatalf("event %s columns changed on no-op re-ingest:\n before=%+v\n after =%+v", e.EventID, before[e.EventID], r)
		}
	}
}

// TestFastPathStillBackfillsGenMS guards the opposite risk: the fast path must
// not skip a fill that is genuinely due. A Codex turn ingested live carries
// gen_ms=0; the same event_id re-delivered after the turn closes carries the
// measured gen_ms, and it must land (ADR-0009) even though the row already
// exists — without double-counting tokens.
func TestFastPathStillBackfillsGenMS(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	live := codexEvent("turnopen", now)
	live.GenMS, live.TTFTMS = 0, 0 // turn still open at first ingest

	if _, err := s.InsertEventsFrom([]model.Event{live}, now.UnixMilli(), OriginSelf); err != nil {
		t.Fatalf("live insert: %v", err)
	}

	closed := codexEvent("turnopen", now) // gen_ms=5000, ttft=800 once the turn closed
	stored, err := s.InsertEventsFrom([]model.Event{closed}, now.UnixMilli()+1000, OriginSelf)
	if err != nil {
		t.Fatalf("closed insert: %v", err)
	}
	if stored != 0 {
		t.Fatalf("re-insert stored %d rows, want 0", stored)
	}

	var gen, ttft, in, out int64
	if err := s.rdb.QueryRow(`SELECT gen_ms, ttft_ms, input_tokens, output_tokens FROM events WHERE event_id = 'turnopen'`).
		Scan(&gen, &ttft, &in, &out); err != nil {
		t.Fatalf("reread: %v", err)
	}
	if gen != 5000 || ttft != 800 {
		t.Fatalf("gen_ms=%d ttft_ms=%d after close, want 5000/800 (fill must survive the fast path)", gen, ttft)
	}
	if in != 100 || out != 42 {
		t.Fatalf("token counts moved on re-ingest: input=%d output=%d, want 100/42", in, out)
	}
}

// BenchmarkReingestAllDuplicates measures the cost the fast path targets: a
// large Codex batch re-applied verbatim, the steady state of a long live
// session under FullReparse. Every row is a fully-populated duplicate, so the
// ideal cost is one existence probe per batch, not a per-row UPDATE cascade.
func BenchmarkReingestAllDuplicates(b *testing.B) {
	s, err := Open(b.TempDir() + "/t.db")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	batch := make([]model.Event, 1040)
	for i := range batch {
		batch[i] = codexEvent(uniqueID(i), now.Add(-time.Duration(i)*time.Second))
	}
	if _, err := s.InsertEventsFrom(batch, now.UnixMilli(), OriginSelf); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.InsertEventsFrom(batch, now.UnixMilli(), OriginSelf); err != nil {
			b.Fatal(err)
		}
	}
}

func uniqueID(i int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := []byte("ev------")
	for j := 2; j < len(out); j++ {
		out[j] = digits[i%len(digits)]
		i /= len(digits)
	}
	return string(out)
}
