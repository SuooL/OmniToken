package server

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

func capacityQuota(scope string, windowMinutes int, pct float64, resets time.Time) model.QuotaSnapshot {
	return model.QuotaSnapshot{
		Device: "mac", Source: "claude-code", LimitID: "claude-account", Scope: scope,
		WindowMinutes: windowMinutes, UsedPercent: pct,
		ResetsAt: resets.UnixMilli(), ObservedAt: time.Now().UnixMilli(),
	}
}

// seedCapacitySamples lays down n past windows that each imply
// tokens/pct × 100, which is what the learned prior will be built from.
func seedCapacitySamples(t *testing.T, s *Server, scope string, windowMinutes int, before time.Time, tokens int64) {
	t.Helper()
	for i := 1; i <= 3; i++ {
		if err := s.store.ObserveCapacity(store.CapacityObservation{
			Source: "claude-code", Scope: scope, WindowMinutes: windowMinutes,
			ResetsAt:    before.Add(-time.Duration(i) * 6 * time.Hour).UnixMilli(),
			UsedPercent: 50, Tokens: tokens,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Nothing learned yet AND a percentage too coarse to divide by: the card
// carries the percentage and the tokens and says nothing about the allowance.
// At 5% the half-point of rounding is ±10%, and the window has barely opened
// (ADR-0034).
func TestWindowCardWithholdsCapacityWhenNothingCanSay(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 5, now.Add(3*time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	if card.UsedPercent != 5 {
		t.Fatalf("used_percent = %v, want the authoritative 5", card.UsedPercent)
	}
	if card.CapacityTokens != 0 || card.RemainingTokens != 0 {
		t.Errorf("capacity invented with no prior and a 5%% reading: %+v", card)
	}
}

// With nothing learned, a window that is far enough along answers for itself:
// 100 tokens at 40% implies 250. The alternative — saying nothing until three
// past windows exist — leaves a brand-new install with no allowance for days
// while the provider is telling it the answer every few seconds.
func TestWindowCardUsesTheLiveWindowWhenUncalibrated(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 40, now.Add(3*time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	if card.Tokens != 100 {
		t.Fatalf("tokens = %d, want the seeded 100", card.Tokens)
	}
	if card.CapacityTokens != 250 || card.RemainingTokens != 150 {
		t.Errorf("capacity/remaining = %d/%d, want 250/150 from 100 tokens at 40%%",
			card.CapacityTokens, card.RemainingTokens)
	}
}

// The whole point of ADR-0034: what the card says must agree with the
// authoritative percentage beside it. The prior here implies a 1000-token
// window, the live one implies 250, and at 40% the live reading — whose only
// quantified error is ±1.25% of rounding — has to win by a wide margin.
func TestWindowCardAnchorsOnTheAuthoritativePercentage(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	resets := now.Add(3 * time.Hour)
	seedCapacitySamples(t, s, "five_hour", 300, resets, 500) // each implies 1000

	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 40, resets),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	// Blended, so not exactly 250 — but nowhere near the prior's 1000, which is
	// the number the old rule put on screen next to "40% used".
	if card.CapacityTokens < 245 || card.CapacityTokens > 260 {
		t.Errorf("capacity = %d, want ≈250 (100 tokens at 40%%), not the prior's 1000",
			card.CapacityTokens)
	}
	if card.RemainingTokens != card.CapacityTokens-100 {
		t.Errorf("remaining = %d, want capacity minus the 100 already used", card.RemainingTokens)
	}
}

// A young window leans on what was learned instead: at 1% the rounding alone is
// ±50%, so this window's own ratio says almost nothing.
func TestWindowCardLeansOnThePriorWhileTheWindowIsYoung(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	resets := now.Add(4 * time.Hour)
	seedCapacitySamples(t, s, "five_hour", 300, resets, 500) // each implies 1000

	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 1, resets),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	// The live ratio here is 100/0.01 = 10000, wildly high; the prior is 1000.
	// The prior must dominate, but not to the point of ignoring the window.
	if card.CapacityTokens < 1000 || card.CapacityTokens > 3500 {
		t.Errorf("capacity = %d, want the prior (1000) to dominate a 1%% reading", card.CapacityTokens)
	}
}

// A window that has outrun the estimate reports nothing left rather than a
// negative number.
func TestWindowCardClampsRemainingAtZero(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	resets := now.Add(time.Hour)
	seedCapacitySamples(t, s, "five_hour", 300, resets, 20) // each implies 40

	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 100, resets),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	if card.CapacityTokens > card.Tokens {
		t.Fatalf("capacity = %d, want no more than the %d already spent at 100%%",
			card.CapacityTokens, card.Tokens)
	}
	if card.RemainingTokens != 0 {
		t.Errorf("remaining = %d, want 0 — the window is already past the estimate", card.RemainingTokens)
	}
}

// The panel must not write. Rendering used to deposit the sample, which meant a
// window that filled while nobody had the page open taught the estimate
// nothing — and the response cache swallowed most of the rest (ADR-0034).
func TestBuildingCardsDoesNotWriteSamples(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	quotas := []model.QuotaSnapshot{capacityQuota("five_hour", 300, 40, now.Add(3*time.Hour))}
	for i := 0; i < 3; i++ {
		if _, err := s.buildWindowCards(now, quotas); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := s.store.CapacitySamples("claude-code", "five_hour", 300, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Errorf("rendering deposited %d samples: %+v", len(samples), samples)
	}
}

// The weekly window gets its own card, and only when the provider reports one:
// a rolling 7-day look-back is not a quota window and would invite being read
// as one.
func TestWeeklyCardAppearsOnlyWithAWeeklyQuota(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))

	cards, err := s.buildWindowCards(now, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.Key == "claude-code:weekly" {
			t.Fatal("weekly card built with no weekly quota reported")
		}
	}

	cards, err = s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("seven_day", 10080, 22, now.Add(100*time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	weekly := cardByKey(t, cards, "claude-code:weekly")
	if !weekly.Authoritative || weekly.UsedPercent != 22 {
		t.Errorf("weekly card = %+v, want an authoritative 22%%", weekly)
	}
	if weekly.WindowMinutes != 10080 {
		t.Errorf("window_minutes = %d, want 10080", weekly.WindowMinutes)
	}
	// The five-hour card must still be addressable by the bare source key.
	if c := cardByKey(t, cards, "claude-code"); c.WindowMinutes != 300 {
		t.Errorf("five-hour card = %+v, want window_minutes 300", c)
	}
}

// Calibration is per scope: a weekly allowance learned from weekly windows must
// never be offered as a five-hour one.
func TestCapacityDoesNotLeakAcrossWindowLengths(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	for i := 1; i <= 4; i++ {
		if err := s.store.ObserveCapacity(store.CapacityObservation{
			Source: "claude-code", Scope: "seven_day", WindowMinutes: 10080,
			ResetsAt:    now.Add(-time.Duration(i) * 8 * 24 * time.Hour).UnixMilli(),
			UsedPercent: 50, Tokens: 5000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 40, now.Add(time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The five-hour card has no prior of its own, so it answers from its own
	// live reading (100 tokens at 40% → 250). What it must never do is reach for
	// the weekly samples, which imply 10000.
	if card := cardByKey(t, cards, "claude-code"); card.CapacityTokens != 250 {
		t.Errorf("five-hour capacity = %d, want 250 from its own window — the weekly calibration implies 10000",
			card.CapacityTokens)
	}
}

func cardByKey(t *testing.T, cards []windowCard, key string) windowCard {
	t.Helper()
	for _, c := range cards {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no card with key %q", key)
	return windowCard{}
}

// A burn rate is only worth extrapolating over a span comparable to the one it
// was measured on. The weekly card made this visible: 90 minutes of traffic ran
// out over the remaining 166 hours announced 980%, which says nothing except
// that the window had barely started.
func TestProjectionWaitsUntilEnoughOfTheWindowHasElapsed(t *testing.T) {
	now := time.Now()
	weekMinutes := 7 * 24 * 60

	early := windowCard{
		Tokens: 15_000_000, UsedPercent: 9,
		ResetsAt: now.Add(time.Duration(weekMinutes-90) * time.Minute).UnixMilli(),
	}
	early.projectToWindowEnd(now, now.Add(-90*time.Minute))
	if early.ProjectedTokens != 0 {
		t.Errorf("projected from 90 minutes of a week: %d (%.0f%%)",
			early.ProjectedTokens, early.ProjectedPercent)
	}

	// A quarter of the way in, the rate has a span behind it worth using.
	elapsed := time.Duration(weekMinutes/4) * time.Minute
	mature := windowCard{
		Tokens: 15_000_000, UsedPercent: 9,
		ResetsAt: now.Add(time.Duration(weekMinutes)*time.Minute - elapsed).UnixMilli(),
	}
	mature.projectToWindowEnd(now, now.Add(-elapsed))
	if mature.ProjectedTokens == 0 {
		t.Error("no projection a quarter of the way through the window")
	}
}
