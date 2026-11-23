package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

func TestClassifyAntigravityModel(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"gemini-4-neon-high", AntigravityGroupGeminiPro},
		{"gemini-3.5-pro-preview", AntigravityGroupGeminiPro},
		{"google/gemini-2.5-pro", AntigravityGroupGeminiPro},
		{"vertex_ai/gemini-4-pro", AntigravityGroupGeminiPro},
		{"gemini-2.5-flash", AntigravityGroupGeminiFlash},
		{"google/gemini-3-flash-lite", AntigravityGroupGeminiFlash},
		{"models/gemini-flash-latest", AntigravityGroupGeminiFlash},
		{"claude-sonnet-4-6", AntigravityGroupThirdParty},
		{"anthropic/claude-opus-4-6", AntigravityGroupThirdParty},
		{"gpt-5.4", AntigravityGroupThirdParty},
		{"openai/o3-mini", AntigravityGroupThirdParty},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := ClassifyAntigravityModel(tc.model); got != tc.want {
			t.Errorf("ClassifyAntigravityModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestAntigravityQuotaGroupsRollingAndBudgets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	events := []model.Event{
		// Within 5h: Gemini Pro / 4
		{
			EventID: "agy:1", TS: now.Add(-1 * time.Hour).UnixMilli(),
			Device: "mac", Source: "antigravity", Provider: "google",
			Model: "gemini-4-neon-high", InputTokens: 1_000_000, OutputTokens: 200_000, CacheReadTokens: 3_800_000,
		},
		// Within 5h: Gemini Flash
		{
			EventID: "agy:2", TS: now.Add(-2 * time.Hour).UnixMilli(),
			Device: "mac", Source: "antigravity", Provider: "google",
			Model: "gemini-2.5-flash", InputTokens: 500_000, OutputTokens: 100_000, CacheReadTokens: 400_000,
		},
		// Within 7d but outside 5h: Claude / Third-Party
		{
			EventID: "agy:3", TS: now.Add(-24 * time.Hour).UnixMilli(),
			Device: "mac", Source: "antigravity", Provider: "anthropic",
			Model: "claude-sonnet-4-6", InputTokens: 2_000_000, OutputTokens: 500_000, CacheReadTokens: 500_000,
		},
		// Within 5h: missing model -> unclassified footnote, must NOT add a 4th group
		{
			EventID: "agy:4", TS: now.Add(-30 * time.Minute).UnixMilli(),
			Device: "mac", Source: "antigravity", Provider: "google",
			Model: "", InputTokens: 50_000, OutputTokens: 10_000,
		},
		// Older than 7d: excluded
		{
			EventID: "agy:old", TS: now.Add(-8 * 24 * time.Hour).UnixMilli(),
			Device: "mac", Source: "antigravity", Provider: "google",
			Model: "gemini-4-neon-high", InputTokens: 9_000_000,
		},
		// Other source: excluded
		{
			EventID: "cc:1", TS: now.Add(-10 * time.Minute).UnixMilli(),
			Device: "mac", Source: "claude-code", Provider: "anthropic-oauth",
			Model: "claude-sonnet-4-6", InputTokens: 9_000_000,
		},
	}
	if _, err := st.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st, cfg: &Config{}, prices: prices, bcast: newBroadcaster()}

	// 1. Default unbudgeted mode: no fabricated percentages or remaining tokens.
	payload, err := srv.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	rawGroups, ok := payload["quota_groups"].([]QuotaGroupSummary)
	if !ok || len(rawGroups) != 1 {
		t.Fatalf("quota_groups = %#v, want 1 summary", payload["quota_groups"])
	}
	ag := rawGroups[0]
	if ag.Source != "antigravity" || ag.Basis != "rolling_estimate" {
		t.Fatalf("unexpected header: %+v", ag)
	}
	if len(ag.Groups) != 3 {
		t.Fatalf("expected exactly 3 official groups, got %d", len(ag.Groups))
	}
	if ag.FiveHourTokens != 6_060_000 {
		t.Fatalf("FiveHourTokens = %d, want 6060000", ag.FiveHourTokens)
	}
	if ag.WeeklyTokens != 9_060_000 {
		t.Fatalf("WeeklyTokens = %d, want 9060000", ag.WeeklyTokens)
	}
	if ag.UnclassifiedFiveHourTokens != 60_000 || ag.UnclassifiedWeeklyTokens != 60_000 {
		t.Fatalf("unclassified = (%d, %d), want (60000, 60000)", ag.UnclassifiedFiveHourTokens, ag.UnclassifiedWeeklyTokens)
	}
	if ag.FiveHourUsedPercent != nil || ag.FiveHourRemainingTokens != nil || ag.WeeklyUsedPercent != nil || ag.WeeklyRemainingTokens != nil {
		t.Fatalf("unbudgeted mode must not fabricate percentages or remainders: %+v", ag)
	}
	if ag.Groups[0].ID != AntigravityGroupGeminiPro || ag.Groups[0].FiveHourTokens != 5_000_000 || len(ag.Groups[0].Models) != 1 {
		t.Fatalf("unexpected gemini-pro group: %+v", ag.Groups[0])
	}
	if ag.Groups[1].ID != AntigravityGroupGeminiFlash || ag.Groups[1].FiveHourTokens != 1_000_000 {
		t.Fatalf("unexpected gemini-flash group: %+v", ag.Groups[1])
	}
	if ag.Groups[2].ID != AntigravityGroupThirdParty || ag.Groups[2].FiveHourTokens != 0 || ag.Groups[2].WeeklyTokens != 3_000_000 {
		t.Fatalf("unexpected third-party group: %+v", ag.Groups[2])
	}

	// 2. Budgeted mode: 5h remaining clamped by weekly remaining, unbudgeted group excluded from budget sum.
	srv.cfg.AntigravityQuotaBudgets = map[string]AntigravityGroupBudget{
		AntigravityGroupGeminiPro:   {FiveHourTokens: 10_000_000, WeeklyTokens: 6_000_000}, // weekly rem=1M < 5h raw rem=5M -> clamped to 1M
		AntigravityGroupGeminiFlash: {FiveHourTokens: 2_000_000, WeeklyTokens: 20_000_000}, // 5h rem=1M, weekly rem=19M
	}
	payload2, err := srv.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	ag2 := payload2["quota_groups"].([]QuotaGroupSummary)[0]
	if ag2.BudgetConfiguredGroups != 2 {
		t.Fatalf("BudgetConfiguredGroups = %d, want 2", ag2.BudgetConfiguredGroups)
	}
	if ag2.Groups[0].FiveHourRemainingTokens == nil || *ag2.Groups[0].FiveHourRemainingTokens != 1_000_000 {
		t.Fatalf("gemini-pro 5h remaining should clamp to weekly remaining 1M, got %v", ag2.Groups[0].FiveHourRemainingTokens)
	}
	if ag2.FiveHourBudgetTokens != 12_000_000 || ag2.FiveHourRemainingTokens == nil || *ag2.FiveHourRemainingTokens != 2_000_000 {
		t.Fatalf("summary 5h budget/remaining = (%d, %v), want (12000000, 2000000)", ag2.FiveHourBudgetTokens, ag2.FiveHourRemainingTokens)
	}
	if ag2.FiveHourUsedPercent == nil || *ag2.FiveHourUsedPercent != 50.0 {
		t.Fatalf("summary 5h used percent = %v, want 50.0", ag2.FiveHourUsedPercent)
	}
	if ag2.Groups[2].FiveHourBudgetTokens != 0 || ag2.Groups[2].FiveHourRemainingTokens != nil {
		t.Fatalf("unbudgeted third-party group must keep nil remainder: %+v", ag2.Groups[2])
	}
}

func TestSettingsAntigravityBudgetsAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		store:     st,
		cfg:       &Config{Listen: "127.0.0.1:8787"},
		prices:    prices,
		bcast:     newBroadcaster(),
		readCache: newRespCache(time.Now),
	}

	// Reject unknown group key.
	badBody := []byte(`{"antigravity_quota_budgets":{"other":{"five_hour_tokens":1000}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(badBody))
	rec := httptest.NewRecorder()
	srv.handlePutSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown group, got %d: %s", rec.Code, rec.Body.String())
	}

	// Save valid budgets via settings API.
	goodBody := []byte(`{"antigravity_quota_budgets":{"gemini-pro":{"five_hour_tokens":50000000,"weekly_tokens":300000000},"third-party":{"weekly_tokens":80000000}}}`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(goodBody))
	rec = httptest.NewRecorder()
	srv.handlePutSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET settings returns stored budgets.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	rec = httptest.NewRecorder()
	srv.handleGetSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d", rec.Code)
	}
	var got settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AntigravityQuotaBudgets[AntigravityGroupGeminiPro].FiveHourTokens != 50_000_000 ||
		got.AntigravityQuotaBudgets[AntigravityGroupThirdParty].WeeklyTokens != 80_000_000 {
		t.Fatalf("unexpected stored budgets: %+v", got.AntigravityQuotaBudgets)
	}

	// Live payload picks up the saved budgets immediately.
	live, err := srv.livePayload(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ag := live["quota_groups"].([]QuotaGroupSummary)[0]
	if ag.FiveHourBudgetTokens != 50_000_000 || ag.WeeklyBudgetTokens != 380_000_000 {
		t.Fatalf("live budgets = (%d, %d), want (50000000, 380000000)", ag.FiveHourBudgetTokens, ag.WeeklyBudgetTokens)
	}
}
