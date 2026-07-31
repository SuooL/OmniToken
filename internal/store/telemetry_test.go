package store

import (
	"math"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func telemetryEvent(id string, ts time.Time, source, modelID string, tokens int64) model.Event {
	return model.Event{
		EventID: id, TS: ts.UnixMilli(), Device: "mac", Source: source,
		Model: modelID, SessionID: "session-" + id, InputTokens: tokens,
	}
}

func TestTelemetryUsageRespectsTodayAndRollingWindowBoundaries(t *testing.T) {
	st := speedStore(t)
	loc := time.FixedZone("test", -4*60*60)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, loc)
	midnight := time.Date(2026, 7, 30, 0, 0, 0, 0, loc)

	events := []model.Event{
		telemetryEvent("before-midnight", midnight.Add(-time.Millisecond), "claude-code", "old", 999),
		telemetryEvent("at-midnight", midnight, "claude-code", "anthropic.claude-opus-4-8", 100),
		telemetryEvent("at-10h", now.Add(-10*time.Hour), "codex", "gpt-5", 200),
		telemetryEvent("before-5h", now.Add(-5*time.Hour-time.Millisecond), "codex", "gpt-5", 300),
		telemetryEvent("at-5h", now.Add(-5*time.Hour), "claude-code", "claude-opus-4-8", 400),
		telemetryEvent("inside-current", now.Add(-time.Hour), "codex", "gpt-5", 500),
	}
	if _, err := st.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.TelemetryUsage(midnight, now)
	if err != nil {
		t.Fatalf("TelemetryUsage: %v", err)
	}
	if got.Today.TotalTokens != 1_500 {
		t.Errorf("today total = %d, want 1500", got.Today.TotalTokens)
	}
	var modelSum int64
	for _, row := range got.Today.Models {
		modelSum += row.Tokens
	}
	if modelSum != got.Today.TotalTokens {
		t.Errorf("today model sum = %d, total = %d", modelSum, got.Today.TotalTokens)
	}
	if len(got.Today.Models) != 2 ||
		got.Today.Models[0].Model != "gpt-5" || got.Today.Models[0].Tokens != 1_000 ||
		got.Today.Models[1].Model != "claude-opus-4-8" || got.Today.Models[1].Tokens != 500 {
		t.Errorf("today models = %+v, want deterministic canonical rows", got.Today.Models)
	}

	if got.Rolling5H.TotalTokens != 900 {
		t.Errorf("rolling total = %d, want 900", got.Rolling5H.TotalTokens)
	}
	if len(got.Rolling5H.Sources) != 2 {
		t.Fatalf("rolling sources = %+v, want Claude and Codex", got.Rolling5H.Sources)
	}
	bySource := map[string]TelemetrySourceUsage{}
	for _, row := range got.Rolling5H.Sources {
		bySource[row.Source] = row
	}
	if row := bySource["claude-code"]; row.Tokens != 400 || row.PreviousTokens != 0 || row.ChangePercent != nil {
		t.Errorf("Claude window = %+v, want current=400 previous=0 unavailable comparison", row)
	}
	if row := bySource["codex"]; row.Tokens != 500 || row.PreviousTokens != 500 {
		t.Errorf("Codex window = %+v, want current=500 previous=500", row)
	} else if row.ChangePercent == nil || *row.ChangePercent != 0 {
		t.Errorf("Codex change_percent = %v, want 0", row.ChangePercent)
	}
}

func TestTelemetryRollingSourcesReconcileTotalIncludingAPI(t *testing.T) {
	st := speedStore(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	todayStart := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	events := []model.Event{
		telemetryEvent("claude-current", now.Add(-time.Hour), "claude-code", "claude", 100),
		telemetryEvent("codex-current", now.Add(-2*time.Hour), "codex", "gpt", 200),
		telemetryEvent("api-current", now.Add(-3*time.Hour), "proxy", "api-model", 300),
		telemetryEvent("api-previous", now.Add(-7*time.Hour), "openai-api", "api-model", 50),
	}
	if _, err := st.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.TelemetryUsage(todayStart, now)
	if err != nil {
		t.Fatalf("TelemetryUsage: %v", err)
	}
	var sourceSum int64
	bySource := map[string]TelemetrySourceUsage{}
	for _, row := range got.Rolling5H.Sources {
		sourceSum += row.Tokens
		bySource[row.Source] = row
	}
	if sourceSum != got.Rolling5H.TotalTokens {
		t.Fatalf("rolling source sum = %d, total = %d", sourceSum, got.Rolling5H.TotalTokens)
	}
	if row, ok := bySource["api"]; !ok || row.Tokens != 300 || row.PreviousTokens != 50 {
		t.Fatalf("api source = %+v present=%v, want current=300 previous=50", row, ok)
	}
}

func TestTelemetrySpeedSeriesAllocatesTokensAndReportsCoverage(t *testing.T) {
	st := speedStore(t)
	from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)

	claude := telemetryEvent("claude", from.Add(6*time.Minute), "claude-code", "claude-sonnet-4-5", 0)
	claude.OutputTokens = 400
	claude.GenMS = int64((4 * time.Minute) / time.Millisecond)
	claudeUnmeasured := telemetryEvent("claude-unmeasured", from.Add(8*time.Minute), "claude-code", "claude-sonnet-4-5", 0)
	claudeUnmeasured.OutputTokens = 50
	codex := telemetryEvent("codex", from.Add(4*time.Minute), "codex", "gpt-5", 0)
	codex.OutputTokens = 500
	codex.GenMS = int64(time.Minute / time.Millisecond)
	api := telemetryEvent("api", from.Add(9*time.Minute), "proxy", "claude-sonnet-4-5", 0)
	api.OutputTokens = 100
	api.GenMS = int64(time.Minute / time.Millisecond)
	if _, err := st.InsertEvents([]model.Event{claude, claudeUnmeasured, codex, api}, to.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.TelemetrySpeedSeries(from, to, 5*time.Minute)
	if err != nil {
		t.Fatalf("TelemetrySpeedSeries: %v", err)
	}
	if len(got.Series) != 2 {
		t.Fatalf("series buckets = %d, want 2", len(got.Series))
	}
	if got.Series[0].Sources[0].Key != "claude-code" || got.Series[0].Sources[0].OutputTokens != 300 {
		t.Errorf("first bucket sources = %+v, want 300 Claude tokens", got.Series[0].Sources)
	}
	secondTokens := map[string]int64{}
	for _, row := range got.Series[1].Sources {
		secondTokens[row.Key] = row.OutputTokens
	}
	if secondTokens["claude-code"] != 100 || secondTokens["api"] != 100 {
		t.Errorf("second bucket tokens = %+v, want claude-code=100 api=100", secondTokens)
	}
	for _, bucket := range got.Series {
		var sum float64
		for _, source := range bucket.Sources {
			sum += source.ContributionTPS
		}
		if math.Abs(sum-bucket.AggregateTPS) > 1e-9 {
			t.Errorf("bucket %d source sum = %v, aggregate = %v", bucket.StartMS, sum, bucket.AggregateTPS)
		}
	}
	if len(got.MeasuredSources) != 2 || got.MeasuredSources[0] != "api" || got.MeasuredSources[1] != "claude-code" {
		t.Errorf("measured_sources = %v, want [api claude-code]", got.MeasuredSources)
	}
	if len(got.UnmeasuredSources) != 1 || got.UnmeasuredSources[0] != "codex" {
		t.Errorf("unmeasured_sources = %v, want [codex]", got.UnmeasuredSources)
	}
}

func TestTelemetrySpeedSeriesConservesIndivisibleTokensAcrossBuckets(t *testing.T) {
	tests := []struct {
		name       string
		tokens     int64
		eventStart time.Duration
		eventEnd   time.Duration
		wantActive []int64
	}{
		{
			name:       "five tokens across three unequal bucket overlaps",
			tokens:     5,
			eventStart: 30 * time.Second,
			eventEnd:   150 * time.Second,
			wantActive: []int64{30_000, 60_000, 30_000},
		},
		{
			name:       "one token across two buckets",
			tokens:     1,
			eventStart: 30 * time.Second,
			eventEnd:   90 * time.Second,
			wantActive: []int64{30_000, 30_000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := speedStore(t)
			from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
			to := from.Add(time.Duration(len(tc.wantActive)) * time.Minute)
			event := telemetryEvent("indivisible", from.Add(tc.eventEnd), "claude-code", "claude", 0)
			event.OutputTokens = tc.tokens
			event.GenMS = (tc.eventEnd - tc.eventStart).Milliseconds()
			if _, err := st.InsertEvents([]model.Event{event}, to.UnixMilli()); err != nil {
				t.Fatalf("InsertEvents: %v", err)
			}

			got, err := st.TelemetrySpeedSeries(from, to, time.Minute)
			if err != nil {
				t.Fatalf("TelemetrySpeedSeries: %v", err)
			}
			if len(got.Series) != len(tc.wantActive) {
				t.Fatalf("series buckets = %d, want %d", len(got.Series), len(tc.wantActive))
			}

			var allocated int64
			for i, bucket := range got.Series {
				if bucket.ActiveMS != tc.wantActive[i] {
					t.Errorf("bucket %d active_ms = %d, want %d", i, bucket.ActiveMS, tc.wantActive[i])
				}
				for _, source := range bucket.Sources {
					allocated += source.OutputTokens
				}
			}
			if allocated != tc.tokens {
				t.Errorf("allocated output tokens = %d, want conserved total %d", allocated, tc.tokens)
			}
		})
	}
}

func TestTelemetrySpeedSeriesSortsBucketSourcesByKey(t *testing.T) {
	st := speedStore(t)
	from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	to := from.Add(5 * time.Minute)

	claude := telemetryEvent("claude-order", to, "claude-code", "claude-sonnet-4-5", 0)
	claude.OutputTokens = 100
	claude.GenMS = int64(time.Minute / time.Millisecond)
	proxy := telemetryEvent("proxy-order", to, "proxy", "claude-sonnet-4-5", 0)
	proxy.OutputTokens = 900
	proxy.GenMS = int64(time.Minute / time.Millisecond)
	if _, err := st.InsertEvents([]model.Event{claude, proxy}, to.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := st.TelemetrySpeedSeries(from, to, 5*time.Minute)
	if err != nil {
		t.Fatalf("TelemetrySpeedSeries: %v", err)
	}
	if len(got.Series) != 1 || len(got.Series[0].Sources) != 2 {
		t.Fatalf("series = %+v, want one bucket with two sources", got.Series)
	}
	if got.Series[0].Sources[0].Key != "api" || got.Series[0].Sources[1].Key != "claude-code" {
		t.Errorf("source order = %+v, want stable key order independent of contribution", got.Series[0].Sources)
	}
}
