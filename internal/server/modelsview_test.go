package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// The model page prices by the id the tool reported and displays by the folded
// name — in that order.
//
// `anthropic.claude-sonnet-4-20250514` is the case that proves it matters:
// folded it becomes `claude-sonnet-4`, which LiteLLM does not carry at all, so
// pricing the folded name dropped the model's whole spend and then reported the
// model as unpriced. The reported id resolves fine.
func TestModelsViewPricesReportedIDThenFolds(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	ev := func(id, mdl string, out int64) model.Event {
		return model.Event{
			EventID: id, TS: now.Add(-time.Hour).UnixMilli(), Device: "mac",
			Source: "claude-code", Model: mdl, Provider: "anthropic",
			InputTokens: 1000, OutputTokens: out,
		}
	}
	if _, err := s.store.InsertEvents([]model.Event{
		ev("a", "claude-sonnet-4-20250514", 100),
		ev("b", "anthropic.claude-sonnet-4-20250514", 200),
	}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models?days=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		BySource []struct {
			Model   string   `json:"model"`
			Source  string   `json:"source"`
			Events  int64    `json:"events"`
			Output  int64    `json:"output_tokens"`
			CostUSD *float64 `json:"cost_usd"`
		} `json:"by_source"`
		Unpriced []string `json:"unpriced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.BySource) != 1 {
		t.Fatalf("by_source = %+v, want the two variants merged into one bar", got.BySource)
	}
	row := got.BySource[0]
	if row.Model != "claude-sonnet-4" {
		t.Errorf("model = %q, want the folded display name", row.Model)
	}
	if row.Events != 2 || row.Output != 300 {
		t.Errorf("merged sums = %+v, want 2 events / 300 output", row)
	}
	if row.CostUSD == nil || *row.CostUSD <= 0 {
		t.Errorf("cost_usd = %v, want the price of the reported id, not of the folded name", row.CostUSD)
	}
	if len(got.Unpriced) != 0 {
		t.Errorf("unpriced = %v, want empty — both ids are in the pricing table", got.Unpriced)
	}
}

// A model with no price at all still has to be visible, and the id named in
// `unpriced` has to be the one a pricing_overrides entry would match.
func TestModelsViewReportsUnpricedByReportedID(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	if _, err := s.store.InsertEvents([]model.Event{{
		EventID: "u", TS: now.Add(-time.Hour).UnixMilli(), Device: "mac",
		Source: "claude-code", Model: "totally-made-up-model-20260101", Provider: "anthropic",
		InputTokens: 10, OutputTokens: 20,
	}}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models?days=7", nil))
	var got struct {
		BySource []struct {
			Model   string   `json:"model"`
			CostUSD *float64 `json:"cost_usd"`
		} `json:"by_source"`
		Unpriced []string `json:"unpriced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.BySource) != 1 || got.BySource[0].CostUSD != nil {
		t.Fatalf("by_source = %+v, want one row with no cost (never $0)", got.BySource)
	}
	if len(got.Unpriced) != 1 || got.Unpriced[0] != "totally-made-up-model-20260101" {
		t.Errorf("unpriced = %v, want the reported id with its date intact", got.Unpriced)
	}
}
