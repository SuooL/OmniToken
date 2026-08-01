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

// The allowance is stated only once the calibration has enough windows behind
// it. Before that the card carries the percentage and the tokens, and says
// nothing about what the window holds (ADR-0025 §5).
func TestWindowCardWithholdsCapacityUntilCalibrated(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	resets := now.Add(3 * time.Hour)
	quotas := []model.QuotaSnapshot{capacityQuota("five_hour", 300, 40, resets)}

	cards, err := s.buildWindowCards(now, quotas)
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	if card.UsedPercent != 40 {
		t.Fatalf("used_percent = %v, want the authoritative 40", card.UsedPercent)
	}
	if card.CapacityTokens != 0 || card.RemainingTokens != 0 {
		t.Errorf("capacity stated from one window: %+v", card)
	}

	// Three earlier windows that each reached a usable peak.
	for i := 1; i <= 3; i++ {
		if err := s.store.ObserveCapacity(store.CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt:    resets.Add(-time.Duration(i) * 6 * time.Hour).UnixMilli(),
			UsedPercent: 50, Tokens: 500,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cards, err = s.buildWindowCards(now, quotas)
	if err != nil {
		t.Fatal(err)
	}
	card = cardByKey(t, cards, "claude-code")
	// Each sample implies 1000; the live window holds 100 subscription tokens.
	if card.CapacityTokens != 1000 {
		t.Errorf("capacity = %d, want 1000", card.CapacityTokens)
	}
	if card.RemainingTokens != 900 {
		t.Errorf("remaining = %d, want 900 (1000 - 100 already used)", card.RemainingTokens)
	}
}

// A window that has outrun the estimate reports nothing left rather than a
// negative number.
func TestWindowCardClampsRemainingAtZero(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	resets := now.Add(time.Hour)
	for i := 1; i <= 3; i++ {
		if err := s.store.ObserveCapacity(store.CapacityObservation{
			Source: "claude-code", Scope: "five_hour", WindowMinutes: 300,
			ResetsAt:    resets.Add(-time.Duration(i) * 6 * time.Hour).UnixMilli(),
			UsedPercent: 50, Tokens: 20, // implies a 40-token window
		}); err != nil {
			t.Fatal(err)
		}
	}
	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{
		capacityQuota("five_hour", 300, 90, resets),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := cardByKey(t, cards, "claude-code")
	if card.CapacityTokens != 40 {
		t.Fatalf("capacity = %d, want 40", card.CapacityTokens)
	}
	if card.RemainingTokens != 0 {
		t.Errorf("remaining = %d, want 0 — the window is already past the estimate", card.RemainingTokens)
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
	if card := cardByKey(t, cards, "claude-code"); card.CapacityTokens != 0 {
		t.Errorf("five-hour card borrowed the weekly calibration: %d", card.CapacityTokens)
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
