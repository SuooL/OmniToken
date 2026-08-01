package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func capStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A window contributes one sample, taken at the highest percentage it reached.
//
// The percentage is an integer, so the relative error of a sample is 0.5/pct —
// ±50% at 1%, ±1.7% at 30%. Taking the peak is what makes a sample worth
// keeping at all (ADR-0025).
func TestCapacitySampleTracksTheWindowPeak(t *testing.T) {
	s := capStore(t)
	resets := time.Now().Add(2 * time.Hour).UnixMilli()

	for _, obs := range []struct {
		pct    float64
		tokens int64
	}{{20, 100_000_000}, {45, 300_000_000}, {40, 280_000_000}} {
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt: resets, UsedPercent: obs.pct, Tokens: obs.tokens,
		}); err != nil {
			t.Fatal(err)
		}
	}

	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("one window must yield one sample, got %d", len(samples))
	}
	// The peak was 45% at 300M, so the window holds about 666M.
	if samples[0].PeakPercent != 45 || samples[0].TokensAtPeak != 300_000_000 {
		t.Errorf("sample = %+v, want the 45%%/300M peak", samples[0])
	}
}

// A reading that arrives after the peak has passed must not lower the sample:
// tokens keep climbing while the percentage stays put, and letting a later
// reading overwrite the peak would silently inflate the estimate.
func TestCapacitySampleDoesNotRegressBelowThePeak(t *testing.T) {
	s := capStore(t)
	resets := time.Now().Add(time.Hour).UnixMilli()
	obs := func(pct float64, tokens int64) CapacityObservation {
		return CapacityObservation{Source: "codex", Scope: "primary", WindowMinutes: 10080,
			ResetsAt: resets, UsedPercent: pct, Tokens: tokens}
	}
	for _, o := range []CapacityObservation{obs(60, 500_000_000), obs(60, 520_000_000)} {
		if err := s.ObserveCapacity(o); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.CapacitySamples("codex", "primary", 10080, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].TokensAtPeak != 500_000_000 {
		t.Fatalf("got %+v, want the token count from when the peak was first reached", samples)
	}
}

// Windows that never got far enough carry too much quantisation noise to be
// worth keeping: raising the bar from 10% to 30% halved the spread on real data
// (ADR-0025).
func TestCapacitySamplesSkipShallowWindows(t *testing.T) {
	s := capStore(t)
	base := time.Now().UnixMilli()
	for i, pct := range []float64{5, 12, 35, 50} {
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt: base + int64(i)*6*3600*1000, UsedPercent: pct, Tokens: 100_000_000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want the two that reached 30%%: %+v", len(samples), samples)
	}
}

// Sub-minute jitter in a derived boundary is the same window, not a new one —
// the same rule LatestQuotas uses. Codex computes resets_at from "N seconds
// left", so consecutive observations differ by milliseconds; keyed raw, one
// window would deposit hundreds of samples and drown every other window.
func TestCapacitySampleTreatsJitteredBoundariesAsOneWindow(t *testing.T) {
	s := capStore(t)
	resets := time.Now().Add(24 * time.Hour).UnixMilli()
	for i, pct := range []float64{35, 40, 45} {
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "codex", Scope: "primary", WindowMinutes: 10080,
			ResetsAt: resets + int64(i)*7, UsedPercent: pct, Tokens: int64(i+1) * 100_000_000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.CapacitySamples("codex", "primary", 10080, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1 — milliseconds apart is one window", len(samples))
	}
	if samples[0].PeakPercent != 45 {
		t.Errorf("peak = %v, want 45", samples[0].PeakPercent)
	}
}

// Only subscription traffic counts toward a quota window, so only it may feed
// a sample (ADR-0018 §7). The caller supplies the token count; this asserts the
// store keeps whatever scope it is given separate.
func TestCapacitySamplesAreKeyedByScope(t *testing.T) {
	s := capStore(t)
	resets := time.Now().Add(time.Hour).UnixMilli()
	for _, scope := range []string{"five_hour", "seven_day"} {
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "claude-code", Scope: scope, WindowMinutes: 300,
			ResetsAt: resets, UsedPercent: 50, Tokens: 100_000_000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"five_hour", "seven_day"} {
		samples, err := s.CapacitySamples("claude-code", scope, 300, 30, 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != 1 {
			t.Errorf("scope %q got %d samples, want 1", scope, len(samples))
		}
	}
}

// Insertion must be safe to repeat: the collector calls this every cycle.
func TestObserveCapacityIsIdempotent(t *testing.T) {
	s := capStore(t)
	o := CapacityObservation{Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
		ResetsAt: time.Now().Add(time.Hour).UnixMilli(), UsedPercent: 40, Tokens: 200_000_000}
	for i := 0; i < 5; i++ {
		if err := s.ObserveCapacity(o); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d rows, want 1", len(samples))
	}
}

// The estimate is the 25th percentile of the samples, and it is withheld until
// there are enough of them — inventing a number is worse than showing none.
func TestCapacityEstimateNeedsEnoughSamples(t *testing.T) {
	s := capStore(t)
	base := time.Now().UnixMilli()
	add := func(i int, pct float64, tokens int64) {
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt: base + int64(i)*6*3600*1000, UsedPercent: pct, Tokens: tokens,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, 50, 200_000_000) // 400M
	add(1, 50, 250_000_000) // 500M
	if _, ok, err := s.CapacityEstimate("claude-code", "five_hour", 300); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("two samples must not produce an estimate")
	}

	add(2, 50, 300_000_000) // 600M
	add(3, 50, 350_000_000) // 700M
	got, ok, err := s.CapacityEstimate("claude-code", "five_hour", 300)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("four samples must produce an estimate")
	}
	// Sorted: 400M 500M 600M 700M — the 25th percentile is the second.
	if got != 500_000_000 {
		t.Errorf("estimate = %d, want the 25th percentile (500M)", got)
	}
}

// The estimate must follow a plan change on its own. Only the most recent
// windows are considered, so old samples roll out of view without anyone
// clearing anything (ADR-0025 §5).
func TestCapacityEstimateForgetsOldWindows(t *testing.T) {
	s := capStore(t)
	base := time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
	// 30 old windows at ~200M, then 30 recent ones at ~800M.
	for i := 0; i < 60; i++ {
		tokens := int64(100_000_000)
		if i >= 30 {
			tokens = 400_000_000
		}
		if err := s.ObserveCapacity(CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt: base + int64(i)*6*3600*1000, UsedPercent: 50, Tokens: tokens,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.CapacityEstimate("claude-code", "five_hour", 300)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want an estimate")
	}
	if got != 800_000_000 {
		t.Errorf("estimate = %d, want 800M — the old 200M windows must have rolled out", got)
	}
}

// A quota snapshot is only usable as a sample when it names a real window.
func TestObserveCapacityRejectsUnusableObservations(t *testing.T) {
	s := capStore(t)
	for _, o := range []CapacityObservation{
		{Source: "claude-code", Scope: "five_hour", WindowMinutes: 300, ResetsAt: 0, UsedPercent: 50, Tokens: 1},
		{Source: "claude-code", Scope: "five_hour", WindowMinutes: 300, ResetsAt: 1, UsedPercent: 0, Tokens: 1},
		{Source: "claude-code", Scope: "five_hour", WindowMinutes: 300, ResetsAt: 1, UsedPercent: 50, Tokens: 0},
	} {
		if err := s.ObserveCapacity(o); err != nil {
			t.Fatalf("must be a silent no-op, got %v", err)
		}
	}
	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Errorf("got %d samples, want none", len(samples))
	}
}

// Backfill turns history already on disk into samples, so the estimate does not
// have to wait a day (five-hour) or three weeks (weekly) to say anything.
func TestBackfillCapacityDerivesSamplesFromHistory(t *testing.T) {
	s := capStore(t)
	now := time.Now()

	// Two past five-hour windows, each with a run of snapshots and matching
	// subscription events inside the window.
	for w := 1; w <= 2; w++ {
		resets := now.Add(-time.Duration(w) * 6 * time.Hour)
		start := resets.Add(-5 * time.Hour)
		var quotas []model.QuotaSnapshot
		for i, pct := range []float64{10, 35, 50} {
			quotas = append(quotas, model.QuotaSnapshot{
				Device: "mac", Source: "claude-code", LimitID: "claude-account",
				Scope: "five_hour", WindowMinutes: 300, UsedPercent: pct,
				ResetsAt: resets.UnixMilli(), ObservedAt: start.Add(time.Duration(i+1) * time.Hour).UnixMilli(),
			})
		}
		if _, err := s.InsertQuotas(quotas); err != nil {
			t.Fatal(err)
		}
		var events []model.Event
		for i := 0; i < 3; i++ {
			events = append(events, model.Event{
				EventID: "e" + itoaTest(w) + itoaTest(i), TS: start.Add(time.Duration(i*30) * time.Minute).UnixMilli(),
				Device: "mac", Source: "claude-code", Provider: model.ProviderAnthropicOAuth,
				Model: "claude-opus-4-8", InputTokens: 100,
			})
		}
		if _, err := s.InsertEvents(events, now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}

	filled, err := s.BackfillCapacity(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if filled != 2 {
		t.Fatalf("backfilled %d windows, want 2", filled)
	}
	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, capacityMinPeak, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(samples))
	}
	// The peak is 50%, first reached three hours in — by then 300 tokens had
	// been recorded, implying a 600-token window.
	for _, c := range samples {
		if c.PeakPercent != 50 || c.Capacity() != 600 {
			t.Errorf("sample %+v implies %d, want 50%% and 600", c, c.Capacity())
		}
	}
}

// Only subscription traffic counts against a quota window, so relay and
// pay-per-use events must not inflate a backfilled sample (ADR-0018 §7).
func TestBackfillCapacityCountsOnlySubscriptionTraffic(t *testing.T) {
	s := capStore(t)
	now := time.Now()
	resets := now.Add(-6 * time.Hour)
	start := resets.Add(-5 * time.Hour)

	if _, err := s.InsertQuotas([]model.QuotaSnapshot{{
		Device: "mac", Source: "claude-code", LimitID: "claude-account",
		Scope: "five_hour", WindowMinutes: 300, UsedPercent: 50,
		ResetsAt: resets.UnixMilli(), ObservedAt: start.Add(time.Hour).UnixMilli(),
	}}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, provider string, tokens int64) model.Event {
		return model.Event{
			EventID: id, TS: start.Add(10 * time.Minute).UnixMilli(), Device: "mac",
			Source: "claude-code", Provider: provider, Model: "claude-opus-4-8", InputTokens: tokens,
		}
	}
	if _, err := s.InsertEvents([]model.Event{
		mk("sub", model.ProviderAnthropicOAuth, 100),
		mk("relay", model.ProviderRelay, 900),
		mk("api", model.ProviderAnthropicAPI, 900),
	}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackfillCapacity(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	samples, err := s.CapacitySamples("claude-code", "five_hour", 300, capacityMinPeak, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Capacity() != 200 {
		t.Fatalf("got %+v, want one sample implying 200 (subscription only)", samples)
	}
}

func itoaTest(i int) string { return string(rune('a' + i)) }
