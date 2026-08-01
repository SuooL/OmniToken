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
