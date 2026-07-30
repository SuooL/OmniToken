package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// The cache page prices by the id the tool reported and displays by the folded
// name. Both halves matter: fold too early and the price lookup misses, fold
// too late and one model occupies two rows whose rates each describe a slice.
func TestCacheViewMergesModelVariantsAfterPricing(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	ev := func(id, mdl string, input, cacheRead int64) model.Event {
		return model.Event{
			EventID: id, TS: now.Add(-time.Hour).UnixMilli(), Device: "mac",
			Source: "claude-code", Model: mdl, Provider: "anthropic",
			InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: 10,
		}
	}
	if _, err := s.store.InsertEvents([]model.Event{
		ev("a", "claude-sonnet-4-5", 1000, 3000),
		ev("b", "anthropic.claude-sonnet-4-5", 1000, 5000),
	}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleCache(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cache?days=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Models []struct {
			Model     string  `json:"model"`
			Events    int64   `json:"events"`
			Input     int64   `json:"input_tokens"`
			CacheRead int64   `json:"cache_read_tokens"`
			HitRate   float64 `json:"hit_rate"`
			SavedUSD  float64 `json:"saved_usd"`
		} `json:"models"`
		Unpriced []string `json:"unpriced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Models) != 1 {
		t.Fatalf("models = %+v, want one merged row", got.Models)
	}
	m := got.Models[0]
	if m.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want the folded name", m.Model)
	}
	if m.Events != 2 || m.Input != 2000 || m.CacheRead != 8000 {
		t.Errorf("merged sums = %+v, want 2 events / 2000 input / 8000 read", m)
	}
	// 8000 / (8000 + 2000) — recomputed from the merged sums, not the mean of
	// the two rows' rates (which would be 0.7917).
	if m.HitRate < 0.799 || m.HitRate > 0.801 {
		t.Errorf("hit_rate = %.4f, want 0.8 from the merged sums", m.HitRate)
	}
	// Both halves priced: the reported ids reached the pricing table.
	if len(got.Unpriced) != 0 {
		t.Errorf("unpriced = %v, want none — folding must not break the lookup", got.Unpriced)
	}
	if m.SavedUSD <= 0 {
		t.Errorf("saved_usd = %v, want the savings of both halves", m.SavedUSD)
	}
}

// An id with no price is reported verbatim, because that string is what the
// user has to put in pricing_overrides for it to match anything.
func TestCacheViewReportsUnpricedIdsVerbatim(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	if _, err := s.store.InsertEvents([]model.Event{{
		EventID: "x", TS: now.Add(-time.Hour).UnixMilli(), Device: "mac",
		Source: "claude-code", Model: "anthropic.not-a-real-model-9",
		InputTokens: 100, CacheReadTokens: 100,
	}}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	rec := httptest.NewRecorder()
	s.handleCache(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cache?days=7", nil))
	var got struct {
		Unpriced []string `json:"unpriced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Unpriced) != 1 || got.Unpriced[0] != "anthropic.not-a-real-model-9" {
		t.Errorf("unpriced = %v, want the reported id unchanged", got.Unpriced)
	}
}
