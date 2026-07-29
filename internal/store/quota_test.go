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
