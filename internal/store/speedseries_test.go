package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func openSpeedStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/speed.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedGen inserts one generation: it ENDS at end and ran for genMS.
func seedGen(t *testing.T, s *Store, id, device, session, mdl string, end time.Time, genMS, outTok int64) {
	t.Helper()
	ev := model.Event{
		EventID: id, TS: end.UnixMilli(), Device: device, Source: "claude-code",
		Model: mdl, Provider: "anthropic", SessionID: session,
		OutputTokens: outTok, GenMS: genMS,
	}
	if _, err := s.InsertEvents([]model.Event{ev}, end.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

// Idle buckets must be distinguishable from slow ones. A zero here would be a
// claim that something generated and produced nothing.
func TestSpeedSeriesLeavesIdleBucketsEmpty(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	// One 30s generation of 3000 tokens inside the second minute: 100 tok/s.
	seedGen(t, s, "e1", "mac", "s1", "claude-opus-4-8", base.Add(90*time.Second), 30_000, 3000)

	buckets, err := s.SpeedSeries(base, base.Add(3*time.Minute), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(buckets))
	}
	if buckets[0].ActiveMS != 0 || buckets[0].TPS != 0 {
		t.Errorf("idle bucket = %+v, want no activity", buckets[0])
	}
	if buckets[1].ActiveMS != 30_000 {
		t.Errorf("active_ms = %d, want 30000", buckets[1].ActiveMS)
	}
	if got := buckets[1].TPS; got < 99 || got > 101 {
		t.Errorf("tps = %.1f, want ~100", got)
	}
	if buckets[2].ActiveMS != 0 {
		t.Errorf("trailing bucket = %+v, want idle", buckets[2])
	}
}

// A response that spans a boundary belongs to both minutes. Charging it all to
// the minute it finished in would draw a spike that never happened.
func TestSpeedSeriesSplitsIntervalAcrossBuckets(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	// Runs 10:00:30 → 10:01:30 at a steady 100 tok/s.
	seedGen(t, s, "e1", "mac", "s1", "claude-opus-4-8", base.Add(90*time.Second), 60_000, 6000)

	buckets, err := s.SpeedSeries(base, base.Add(2*time.Minute), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range buckets {
		if b.ActiveMS != 30_000 {
			t.Errorf("bucket %d active_ms = %d, want 30000", i, b.ActiveMS)
		}
		if b.OutputTokens != 3000 {
			t.Errorf("bucket %d tokens = %d, want 3000 (half the response)", i, b.OutputTokens)
		}
		if b.TPS < 99 || b.TPS > 101 {
			t.Errorf("bucket %d tps = %.1f, want ~100", i, b.TPS)
		}
	}
}

// Overlapping subagents file under the parent's session id (ADR-0009). Their
// wall clock is shared, so it may be counted once — but the tokens are real.
func TestSpeedSeriesUnionsOverlapWithinBucket(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	// Two 30s generations over the SAME 30 seconds, 3000 tokens each.
	seedGen(t, s, "a", "mac", "s1", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)
	seedGen(t, s, "b", "mac", "s1", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)

	buckets, err := s.SpeedSeries(base, base.Add(time.Minute), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if buckets[0].ActiveMS != 30_000 {
		t.Errorf("active_ms = %d, want 30000 — overlapping spans must union", buckets[0].ActiveMS)
	}
	// 6000 tokens in 30 wall-clock seconds: the machine really did emit 200/s.
	if got := buckets[0].TPS; got < 199 || got > 201 {
		t.Errorf("tps = %.1f, want ~200", got)
	}
}

// Events without a generation interval (Codex, or history predating gen_ms)
// carry no speed information. Treating them as instantaneous would report
// absurd rates; they are excluded from the numerator and shown as coverage.
func TestSpeedIgnoresEventsWithoutInterval(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	seedGen(t, s, "with", "mac", "s1", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)
	seedGen(t, s, "without", "mac", "s1", "claude-opus-4-8", base.Add(40*time.Second), 0, 9999)

	buckets, err := s.SpeedSeries(base, base.Add(time.Minute), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if buckets[0].OutputTokens != 3000 {
		t.Errorf("tokens = %d, want 3000 (the interval-less event must not count)", buckets[0].OutputTokens)
	}

	stats, err := s.SpeedByModelUnion(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one model", stats)
	}
	if stats[0].Samples != 1 {
		t.Errorf("samples = %d, want 1", stats[0].Samples)
	}
	if got := stats[0].Coverage; got < 0.49 || got > 0.51 {
		t.Errorf("coverage = %.2f, want ~0.5 — one of two events had an interval", got)
	}
}

// Per-model speed is a per-stream figure. Two machines generating at 100 tok/s
// at the same time is still a 100 tok/s model, not a 200 tok/s one.
func TestSpeedByModelDoesNotUnionAcrossStreams(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	seedGen(t, s, "m1", "mac", "s1", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)
	seedGen(t, s, "m2", "linux", "s2", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)

	stats, err := s.SpeedByModelUnion(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one model", stats)
	}
	if stats[0].Streams != 2 {
		t.Errorf("streams = %d, want 2", stats[0].Streams)
	}
	if stats[0].ActiveMS != 60_000 {
		t.Errorf("active_ms = %d, want 60000 (two streams' time added, not merged)", stats[0].ActiveMS)
	}
	if got := stats[0].TPS; got < 99 || got > 101 {
		t.Errorf("tps = %.1f, want ~100 — a stream's speed, not the fleet's throughput", got)
	}
}

// Two ids for one model must not split its speed in two rows.
func TestSpeedByModelFoldsVendorPrefixes(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	seedGen(t, s, "p1", "mac", "s1", "claude-opus-4-8", base.Add(30*time.Second), 30_000, 3000)
	seedGen(t, s, "p2", "mac", "s2", "anthropic.claude-opus-4-8", base.Add(90*time.Second), 30_000, 3000)

	stats, err := s.SpeedByModelUnion(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Model != "claude-opus-4-8" {
		t.Fatalf("stats = %+v, want one folded row", stats)
	}
	if stats[0].Samples != 2 {
		t.Errorf("samples = %d, want 2", stats[0].Samples)
	}
}

// Short replies are dominated by the wait for the first token, which a
// log-derived interval cannot separate from generation. They still belong in
// the aggregate — those tokens and that time both happened — but this is why
// no per-response median is published for this channel.
func TestSpeedByModelKeepsShortRepliesInTheAggregate(t *testing.T) {
	s := openSpeedStore(t)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	// A real answer: 1000 tokens in 10s.
	seedGen(t, s, "r1", "mac", "s1", "claude-opus-4-8", base.Add(time.Minute), 10_000, 1000)
	// A two-token tool decision that took a second, almost all of it latency.
	seedGen(t, s, "t1", "mac", "s2", "claude-opus-4-8", base.Add(2*time.Minute), 1000, 2)

	stats, err := s.SpeedByModelUnion(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].Samples != 2 {
		t.Errorf("samples = %d, want 2", stats[0].Samples)
	}
	if stats[0].ActiveMS != 11_000 {
		t.Errorf("active_ms = %d, want 11000", stats[0].ActiveMS)
	}
	// 1002 tokens over 11s. The short reply moves it a little, as it should:
	// that second really was spent waiting on this model.
	if got := stats[0].TPS; got < 91 || got > 92 {
		t.Errorf("tps = %.1f, want ~91.1", got)
	}
}
