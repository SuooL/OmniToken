package server

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// One account's 5-hour window can be reported by every device signed into it,
// and the readings differ because each device observed at a different moment.
// Taking whichever row happened to come last means the panel answers "how full
// is my window" with a number that depends on map iteration order — and the
// menubar's quota card, which leads with this value, would flip between two
// answers on consecutive refreshes.
//
// The tray already resolves this the right way (`gauge::tightest_percent`), so
// picking anything else here also makes the popover and the menubar disagree
// about the same account.
func TestFiveHourQuotaKeepsTheTightestReadingPerSource(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	resets := now.Add(2 * time.Hour).UnixMilli()

	quotas := []model.QuotaSnapshot{
		{Source: "claude-code", Device: "macmini", WindowMinutes: 300, UsedPercent: 23, ResetsAt: resets},
		{Source: "claude-code", Device: "suool-mac", WindowMinutes: 300, UsedPercent: 71, ResetsAt: resets},
		{Source: "claude-code", Device: "laptop", WindowMinutes: 300, UsedPercent: 8, ResetsAt: resets},
	}

	got := tightestQuota(quotas, now, 300)

	w, ok := got["claude-code"]
	if !ok {
		t.Fatalf("claude-code window missing; got %#v", got)
	}
	if w.pct != 71 {
		t.Errorf("used_percent = %v, want 71 (the tightest of 23/71/8)", w.pct)
	}
	if w.resets.UnixMilli() != resets {
		t.Errorf("resets = %v, want %v", w.resets.UnixMilli(), resets)
	}
	if want := w.resets.Add(-fiveHours); !w.start.Equal(want) {
		t.Errorf("start = %v, want %v", w.start, want)
	}
}

// Order must not decide the answer: the same set of readings has to produce the
// same window whichever way round it arrives.
func TestFiveHourQuotaIsIndependentOfArrivalOrder(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	resets := now.Add(90 * time.Minute).UnixMilli()

	ascending := []model.QuotaSnapshot{
		{Source: "codex", WindowMinutes: 300, UsedPercent: 12, ResetsAt: resets},
		{Source: "codex", WindowMinutes: 300, UsedPercent: 64, ResetsAt: resets},
	}
	descending := []model.QuotaSnapshot{
		{Source: "codex", WindowMinutes: 300, UsedPercent: 64, ResetsAt: resets},
		{Source: "codex", WindowMinutes: 300, UsedPercent: 12, ResetsAt: resets},
	}

	if a, d := tightestQuota(ascending, now, 300)["codex"], tightestQuota(descending, now, 300)["codex"]; a.pct != d.pct {
		t.Errorf("order changed the answer: %v vs %v", a.pct, d.pct)
	}
}

// One account can report several scopes for the same weekly window — the
// account-wide `seven_day` alongside per-model `seven_day:opus` — and the card
// absorbs whichever is tightest. It has to name that scope, because the quota
// row above decides what to draw by asking which readings a card already shows;
// getting the scope wrong there would either hide a reading nobody else renders
// or leave the duplicate it was meant to remove.
func TestWeeklyWindowCarriesTheScopeItPicked(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	resets := now.Add(48 * time.Hour).UnixMilli()
	observed := now.Add(-3 * time.Minute).UnixMilli()

	quotas := []model.QuotaSnapshot{
		{Source: "claude-code", Scope: "seven_day", WindowMinutes: 10080, UsedPercent: 30, ResetsAt: resets, ObservedAt: observed},
		{Source: "claude-code", Scope: "seven_day:opus", WindowMinutes: 10080, UsedPercent: 88, ResetsAt: resets, ObservedAt: observed},
	}

	w := tightestQuota(quotas, now, 10080)["claude-code"]
	if w.scope != "seven_day:opus" {
		t.Errorf("scope = %q, want seven_day:opus (the tightest of 30/88)", w.scope)
	}
	if w.observed != observed {
		t.Errorf("observed = %d, want %d", w.observed, observed)
	}
}

// The window card repeats the authoritative percentage and reset that the quota
// row also holds, and adds tokens, cost and the projection on top. So the panel
// draws one card, not two — and to drop the duplicate it needs each card to
// state which reading it absorbed, keyed by (source, window minutes, scope).
//
// The server only labels; it must not filter `quotas[]` itself. The menubar
// reads that same list to raise its 75%/90% alerts, so removing an entry there
// would silently disable the alert for the very window the panel is showing.
func TestWindowCardNamesTheReadingItAbsorbed(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	observed := now.Add(-4 * time.Minute).UnixMilli()

	q := capacityQuota("five_hour", 300, 40, now.Add(3*time.Hour))
	q.ObservedAt = observed

	cards, err := s.buildWindowCards(now, []model.QuotaSnapshot{q})
	if err != nil {
		t.Fatal(err)
	}

	card := cardByKey(t, cards, "claude-code")
	if !card.Authoritative {
		t.Fatalf("card is not authoritative: %+v", card)
	}
	if card.Source != "claude-code" || card.Scope != "five_hour" {
		t.Errorf("absorbed reading = (%q, %q), want (claude-code, five_hour)", card.Source, card.Scope)
	}
	if card.ObservedAt != observed {
		t.Errorf("observed_at = %d, want %d — the row above shows freshness, so the card must too",
			card.ObservedAt, observed)
	}

	// A placeholder card absorbed nothing: there was no reading to begin with,
	// so it must not claim one and make the row hide a snapshot it never showed.
	codex := cardByKey(t, cards, "codex")
	if codex.Authoritative {
		t.Fatalf("codex card unexpectedly authoritative: %+v", codex)
	}
	if codex.Scope != "" || codex.ObservedAt != 0 {
		t.Errorf("placeholder claims a reading: scope=%q observed_at=%d", codex.Scope, codex.ObservedAt)
	}
}

// Rows that describe a different window, or a window that has already rolled
// over, must not be able to win by being the tightest.
func TestFiveHourQuotaIgnoresOtherWindowsAndExpiredOnes(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)

	quotas := []model.QuotaSnapshot{
		{Source: "claude-code", WindowMinutes: 10080, UsedPercent: 99, ResetsAt: now.Add(time.Hour).UnixMilli()},
		{Source: "claude-code", WindowMinutes: 300, UsedPercent: 97, ResetsAt: now.Add(-time.Minute).UnixMilli()},
		{Source: "claude-code", WindowMinutes: 300, UsedPercent: 0, ResetsAt: 0},
		{Source: "claude-code", WindowMinutes: 300, UsedPercent: 31, ResetsAt: now.Add(time.Hour).UnixMilli()},
	}

	if w := tightestQuota(quotas, now, 300)["claude-code"]; w.pct != 31 {
		t.Errorf("used_percent = %v, want 31 — the only live 5h reading", w.pct)
	}
}
