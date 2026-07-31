package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func seedTelemetryEvent(t *testing.T, s *Server, event model.Event, receivedAt time.Time) {
	t.Helper()
	if _, err := s.store.InsertEvents([]model.Event{event}, receivedAt.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

func telemetryRequest(t *testing.T, s *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func TestTelemetryRouteRequiresReadAuthAndValidatesRange(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.Listen = "0.0.0.0:8787"
	s.cfg.ReadToken = "read-secret"
	s.now = func() time.Time {
		return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	}

	if rec := telemetryRequest(t, s, "/api/v1/telemetry?range=5h", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", rec.Code)
	}
	if rec := telemetryRequest(t, s, "/api/v1/telemetry?range=week", "read-secret"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d, want 400", rec.Code)
	}
	if rec := telemetryRequest(t, s, "/api/v1/telemetry?range=5h", "read-secret"); rec.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestTelemetryHandlerReturnsCompleteBoundedReconciledSnapshot(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.Listen = "127.0.0.1:8787"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	claude := model.Event{
		EventID: "claude", TS: now.Add(-2 * time.Minute).UnixMilli(),
		Device: "mac", Source: "claude-code", Model: "anthropic.claude-opus-4-8",
		SessionID: "s1", InputTokens: 100, OutputTokens: 600,
		GenMS: int64(time.Minute / time.Millisecond),
	}
	claudeAlias := model.Event{
		EventID: "claude-alias", TS: now.Add(-30 * time.Minute).UnixMilli(),
		Device: "mac", Source: "claude-code", Model: "claude-opus-4-8",
		SessionID: "s2", InputTokens: 200,
	}
	codex := model.Event{
		EventID: "codex", TS: now.Add(-time.Minute).UnixMilli(),
		Device: "mac", Source: "codex", Model: "gpt-5",
		SessionID: "s3", InputTokens: 300, OutputTokens: 900,
	}
	api := model.Event{
		EventID: "api", TS: now.Add(-90 * time.Second).UnixMilli(),
		Device: "mac", Source: "proxy", Model: "claude-sonnet-4-5",
		SessionID: "s4", OutputTokens: 900, GenMS: int64(30 * time.Second / time.Millisecond),
	}
	for _, event := range []model.Event{claude, claudeAlias, codex, api} {
		seedTelemetryEvent(t, s, event, now)
	}

	rec := telemetryRequest(t, s, "/api/v1/telemetry?range=1h", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() >= telemetryResponseLimit {
		t.Fatalf("response bytes = %d, want below %d", rec.Body.Len(), telemetryResponseLimit)
	}
	var got telemetryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.GeneratedAt != now.UnixMilli() || got.Timezone != "UTC" {
		t.Errorf("generated_at/timezone = %d/%q, want %d/UTC", got.GeneratedAt, got.Timezone, now.UnixMilli())
	}
	var modelSum int64
	for _, row := range got.Today.Models {
		modelSum += row.Tokens
	}
	if got.Today.TotalTokens != 3_000 || modelSum != got.Today.TotalTokens || len(got.Today.Models) != 3 {
		t.Errorf("today models = %+v total=%d, want all 3 canonical models summing exactly", got.Today.Models, got.Today.TotalTokens)
	}
	if got.Speed.Range != "1h" || got.Speed.BucketMS != time.Minute.Milliseconds() || len(got.Speed.Series) != 60 {
		t.Errorf("speed range/bucket/series = %q/%d/%d, want 1h/60000/60", got.Speed.Range, got.Speed.BucketMS, len(got.Speed.Series))
	}
	if len(got.Speed.UnmeasuredSources) != 1 || got.Speed.UnmeasuredSources[0] != "codex" {
		t.Errorf("unmeasured_sources = %v, want [codex]", got.Speed.UnmeasuredSources)
	}
	if len(got.Speed.MeasuredSources) != 2 ||
		got.Speed.MeasuredSources[0] != "api" || got.Speed.MeasuredSources[1] != "claude-code" {
		t.Errorf("measured_sources = %v, want [api claude-code]", got.Speed.MeasuredSources)
	}
	if math.Abs(got.Speed.Aggregate10MTPS-(100.0/6.0)) > 1e-9 {
		t.Errorf("aggregate_10m_tps = %v, want %v", got.Speed.Aggregate10MTPS, 100.0/6.0)
	}
	if got.Speed.PeakTPS != 30 || got.Speed.PeakAt != now.Add(-2*time.Minute).UnixMilli() {
		t.Errorf("peak = %v at %d, want 30 at %d",
			got.Speed.PeakTPS, got.Speed.PeakAt, now.Add(-2*time.Minute).UnixMilli())
	}
	if math.Abs(got.Speed.ActiveRatio-0.025) > 1e-9 {
		t.Errorf("active_ratio = %v, want 0.025", got.Speed.ActiveRatio)
	}
	for _, bucket := range got.Speed.Series {
		var sum float64
		lastKey := ""
		for _, source := range bucket.Sources {
			if source.Key < lastKey {
				t.Errorf("bucket sources not deterministically key-sorted: %+v", bucket.Sources)
			}
			lastKey = source.Key
			sum += source.ContributionTPS
		}
		if math.Abs(sum-bucket.AggregateTPS) > 1e-9 {
			t.Errorf("bucket %d source sum=%v aggregate=%v", bucket.StartMS, sum, bucket.AggregateTPS)
		}
	}
}

func TestTelemetryRangeControlsOnlyBoundedSpeedSeries(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.Listen = "127.0.0.1:8787"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	tests := []struct {
		value    string
		bucketMS int64
		buckets  int
	}{
		{value: "1h", bucketMS: time.Minute.Milliseconds(), buckets: 60},
		{value: "5h", bucketMS: (5 * time.Minute).Milliseconds(), buckets: 60},
		{value: "24h", bucketMS: (30 * time.Minute).Milliseconds(), buckets: 48},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			rec := telemetryRequest(t, s, "/api/v1/telemetry?range="+tc.value, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var got telemetryResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Speed.BucketMS != tc.bucketMS || len(got.Speed.Series) != tc.buckets {
				t.Errorf("bucket/series = %d/%d, want %d/%d",
					got.Speed.BucketMS, len(got.Speed.Series), tc.bucketMS, tc.buckets)
			}
			if got.Rolling5H.StartMS != now.Add(-5*time.Hour).UnixMilli() ||
				got.Rolling5H.EndMS != now.UnixMilli() {
				t.Errorf("rolling_5h changed with range %q: %+v", tc.value, got.Rolling5H)
			}
			if len(got.Speed.UnmeasuredSources) != 1 || got.Speed.UnmeasuredSources[0] != "codex" {
				t.Errorf("empty successful range unmeasured_sources = %v, want [codex]", got.Speed.UnmeasuredSources)
			}
		})
	}
}

func TestTelemetryHandlerRejectsOversizedSnapshotWithoutTruncatingModels(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.Listen = "127.0.0.1:8787"
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	events := make([]model.Event, 7_000)
	padding := strings.Repeat("x", 80)
	for i := range events {
		events[i] = model.Event{
			EventID:     fmt.Sprintf("oversized-%05d", i),
			TS:          now.Add(-time.Minute).UnixMilli(),
			Device:      "mac",
			Source:      "claude-code",
			Model:       fmt.Sprintf("model-%05d-%s", i, padding),
			InputTokens: 1,
		}
	}
	if _, err := s.store.InsertEvents(events, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rec := telemetryRequest(t, s, "/api/v1/telemetry?range=5h", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body bytes=%d, want explicit 500 for oversized complete response", rec.Code, rec.Body.Len())
	}
	if !strings.Contains(rec.Body.String(), "exceeds") {
		t.Fatalf("oversized error = %q, want explicit size error", rec.Body.String())
	}
}
