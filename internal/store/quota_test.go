package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestQuotaScopesDoNotCollide(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Claude reports several weekly scopes at the same instant, all with
	// window_minutes=10080: keying without scope would drop all but one.
	obs := time.Now().UnixMilli()
	qs := []model.QuotaSnapshot{
		{Device: "d", Source: "claude-code", LimitID: "claude-oauth-usage", Scope: "seven_day", WindowMinutes: 10080, UsedPercent: 36, ObservedAt: obs},
		{Device: "d", Source: "claude-code", LimitID: "claude-oauth-usage", Scope: "seven_day_opus", WindowMinutes: 10080, UsedPercent: 12, ObservedAt: obs},
		{Device: "d", Source: "claude-code", LimitID: "claude-oauth-usage", Scope: "seven_day_sonnet", WindowMinutes: 10080, UsedPercent: 5, ObservedAt: obs},
		{Device: "d", Source: "claude-code", LimitID: "claude-oauth-usage", Scope: "five_hour", WindowMinutes: 300, UsedPercent: 97, ObservedAt: obs},
	}
	n, err := s.InsertQuotas(qs)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("inserted %d, want 4 (scopes must not collide)", n)
	}
	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 4 {
		t.Fatalf("LatestQuotas returned %d rows, want 4: %+v", len(latest), latest)
	}
	byScope := map[string]float64{}
	for _, q := range latest {
		byScope[q.Scope] = q.UsedPercent
	}
	for scope, want := range map[string]float64{"seven_day": 36, "seven_day_opus": 12, "seven_day_sonnet": 5, "five_hour": 97} {
		if got := byScope[scope]; got != want {
			t.Errorf("%s = %v, want %v", scope, got, want)
		}
	}
}

func TestQuotaLatestPerScope(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := time.Now().UnixMilli()
	mk := func(scope string, pct float64, obs int64) model.QuotaSnapshot {
		return model.QuotaSnapshot{Device: "d", Source: "codex", LimitID: "codex", Scope: scope,
			WindowMinutes: 10080, UsedPercent: pct, ObservedAt: obs}
	}
	if _, err := s.InsertQuotas([]model.QuotaSnapshot{
		mk("primary", 10, base), mk("primary", 20, base+1000),
		mk("secondary", 30, base), // same window as primary, different scope
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("want 2 rows (newest per scope), got %d: %+v", len(latest), latest)
	}
	for _, q := range latest {
		if q.Scope == "primary" && q.UsedPercent != 20 {
			t.Errorf("primary must be the newest observation, got %v", q.UsedPercent)
		}
		if q.Scope == "secondary" && q.UsedPercent != 30 {
			t.Errorf("secondary = %v, want 30", q.UsedPercent)
		}
	}
}

func TestQuotaInsertIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := model.QuotaSnapshot{Device: "d", Source: "codex", LimitID: "codex", Scope: "primary",
		WindowMinutes: 300, UsedPercent: 42, ObservedAt: 1000}
	if n, _ := s.InsertQuotas([]model.QuotaSnapshot{q}); n != 1 {
		t.Fatalf("first insert = %d, want 1", n)
	}
	if n, _ := s.InsertQuotas([]model.QuotaSnapshot{q}); n != 0 {
		t.Errorf("re-inserting the same observation must be a no-op, got %d", n)
	}
}

// A window's identity does not depend on which channel reported it. Grouping
// by limit_id left the retired channel's last reading on screen beside the
// live one — after ADR-0011 moved Claude quota from OAuth to the status line,
// every window rendered twice.
func TestLatestQuotasCollapsesAcrossChannels(t *testing.T) {
	st, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	old := now.Add(-2 * time.Hour).UnixMilli()
	if _, err := st.InsertQuotas([]model.QuotaSnapshot{
		// The retired OAuth channel's last word.
		{Device: "mac", Source: "claude-code", LimitID: "claude-oauth-usage",
			Scope: "five_hour", WindowMinutes: 300, UsedPercent: 2, ObservedAt: old},
		// The live status-line channel.
		{Device: "mac", Source: "claude-code", LimitID: "claude-account",
			Scope: "five_hour", WindowMinutes: 300, UsedPercent: 59, ObservedAt: now.UnixMilli()},
		// Per-model weekly must stay its own row (the ADR-0007 bug).
		{Device: "mac", Source: "claude-code", LimitID: "claude-account",
			Scope: "seven_day:opus", WindowMinutes: 10080, UsedPercent: 23, ObservedAt: now.UnixMilli()},
	}); err != nil {
		t.Fatalf("InsertQuotas: %v", err)
	}

	got, err := st.LatestQuotas(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("LatestQuotas: %v", err)
	}
	var fiveHour []model.QuotaSnapshot
	scopes := map[string]bool{}
	for _, q := range got {
		scopes[q.Scope] = true
		if q.Scope == "five_hour" {
			fiveHour = append(fiveHour, q)
		}
	}
	if len(fiveHour) != 1 {
		t.Fatalf("five_hour returned %d rows, want 1 (one per window, not per channel)", len(fiveHour))
	}
	if fiveHour[0].UsedPercent != 59 {
		t.Errorf("used_percent = %v, want 59 (the newest observation wins)", fiveHour[0].UsedPercent)
	}
	if !scopes["seven_day:opus"] {
		t.Error("per-model weekly scope was collapsed away")
	}
}

// A reading of a window that has already reset must not displace a reading of
// the window that is currently open, however recently it arrived.
//
// This is the shape it took on a real machine: several Claude Code sessions run
// at once, each with its own status-line hook, and a long-lived one keeps
// reporting the account state it captured hours ago — a 5h window whose
// resets_at is already in the past. That reading is observed *now*, so ordering
// by observed_at alone let it win, and the live view (which correctly drops
// windows that have already reset) then had no five_hour row at all. The panel
// alternated between showing the 5h number and "无官方 5h 数据" every couple of
// seconds, depending on which session reported last.
//
// resets_at identifies *which window* a reading is about; observed_at only says
// when someone looked. The window has to be compared first.
func TestLatestQuotasPrefersTheOpenWindowOverAStaleOne(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UnixMilli()
	current := now + 2*60*60*1000 // this window resets in two hours
	expired := now - 2*60*60*1000 // that one reset two hours ago
	mk := func(pct float64, resets, obs int64) model.QuotaSnapshot {
		return model.QuotaSnapshot{Device: "mac", Source: "claude-code", LimitID: "claude-account",
			Scope: "five_hour", WindowMinutes: 300, UsedPercent: pct, ResetsAt: resets, ObservedAt: obs}
	}
	// The stale reading is the most recent one, by a full second.
	if _, err := s.InsertQuotas([]model.QuotaSnapshot{
		mk(7, current, now),
		mk(24, expired, now+1000),
	}); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 {
		t.Fatalf("want 1 row for one (device, source, scope, window), got %d: %+v", len(latest), latest)
	}
	if latest[0].ResetsAt != current || latest[0].UsedPercent != 7 {
		t.Errorf("got %.0f%% resetting at %d, want the open window (7%%, %d)",
			latest[0].UsedPercent, latest[0].ResetsAt, current)
	}
}

// Within one window, the newest look still wins — that is the whole point of
// polling, and the rule above must not freeze a window at its first reading.
func TestLatestQuotasStillTakesTheNewestLookAtOneWindow(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UnixMilli()
	resets := now + 60*60*1000
	mk := func(pct float64, obs int64) model.QuotaSnapshot {
		return model.QuotaSnapshot{Device: "mac", Source: "claude-code", LimitID: "claude-account",
			Scope: "five_hour", WindowMinutes: 300, UsedPercent: pct, ResetsAt: resets, ObservedAt: obs}
	}
	if _, err := s.InsertQuotas([]model.QuotaSnapshot{mk(12, now), mk(19, now+5000)}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].UsedPercent != 19 {
		t.Fatalf("got %+v, want the 19%% reading", latest)
	}
}

// A source that reports no boundary at all must not be permanently outranked
// by nothing: with every resets_at at zero the rule has to fall back to time.
func TestLatestQuotasFallsBackToTimeWhenNoWindowBoundaryIsReported(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UnixMilli()
	mk := func(pct float64, obs int64) model.QuotaSnapshot {
		return model.QuotaSnapshot{Device: "mac", Source: "codex", LimitID: "codex",
			Scope: "primary", WindowMinutes: 10080, UsedPercent: pct, ObservedAt: obs}
	}
	if _, err := s.InsertQuotas([]model.QuotaSnapshot{mk(40, now), mk(55, now+3000)}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].UsedPercent != 55 {
		t.Fatalf("got %+v, want the newest (55%%)", latest)
	}
}

// A window boundary that is derived rather than stated jitters, and the jitter
// must not be read as a newer window.
//
// Codex reports "resets in N seconds", so every observation computes a slightly
// different absolute instant — readings milliseconds apart on the same window
// produced resets_at values that differ by a few milliseconds. Comparing those
// raw let a stale reading win by being 5ms "later": on real data the pick was a
// 88% reading from 00:45 over a 96% reading from 01:39, both of the same weekly
// window. Window identity is therefore compared at minute resolution — far
// finer than the hours that separate real windows, far coarser than the jitter.
func TestLatestQuotasIgnoresSubMinuteJitterInDerivedResetTimes(t *testing.T) {
	s, err := Open(t.TempDir() + "/q.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UnixMilli()
	resets := now + 3*24*60*60*1000
	mk := func(pct float64, resets, obs int64) model.QuotaSnapshot {
		return model.QuotaSnapshot{Device: "mac", Source: "codex", LimitID: "codex",
			Scope: "primary", WindowMinutes: 10080, UsedPercent: pct, ResetsAt: resets, ObservedAt: obs}
	}
	if _, err := s.InsertQuotas([]model.QuotaSnapshot{
		mk(96, resets, now),           // the newest look at this window
		mk(88, resets+5, now-3000000), // an older look whose resets_at is 5ms later
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestQuotas(time.UnixMilli(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(latest), latest)
	}
	if latest[0].UsedPercent != 96 {
		t.Errorf("got %.0f%%, want 96%% — sub-minute jitter is not a new window", latest[0].UsedPercent)
	}
}
