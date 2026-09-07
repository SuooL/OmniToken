package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

// Model page (F22 / GAP-4). Answers one question the overview's flat bars
// cannot: which tool produced each model's tokens, and how the model mix
// moves day to day.

// modelSourceEntry is one (model, source) slice of the stacked bars. CostUSD
// is a pointer so an unpriced model omits the field entirely — a missing
// price must never render as $0 (ADR-0005); those models are listed under
// "unpriced" instead.
type modelSourceEntry struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	store.Totals
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// addTotals accumulates one aggregate into another, used when two routing
// variants of a model fold into a single bar.
func addTotals(dst *store.Totals, src store.Totals) {
	dst.Events += src.Events
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheRead += src.CacheRead
	dst.CacheCreation += src.CacheCreation
	dst.TotalTokens += src.TotalTokens
}

// modelDailyEntry is one (day, model) segment of the daily composition chart.
// Models outside the top N arrive pre-merged as store.ModelOther.
type modelDailyEntry struct {
	Bucket string `json:"bucket"`
	Model  string `json:"model"`
	store.Totals
}

// handleModels answers GET /api/v1/models?days=30&top=6.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	topN := queryInt(r, "top", 6)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	rows, err := s.store.ModelBySource(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	daily, err := s.store.ModelDaily(rangeStart, end, topN)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Price each reported id, then fold the routing variants into one bar —
	// in that order, because a display name need not exist in the pricing table
	// (internal/model/canonical.go). ModelBySource already orders rows so the
	// variants of one slice arrive together, so first-seen order is the display
	// order.
	bySource := make([]modelSourceEntry, 0, len(rows))
	index := map[[2]string]int{}
	unpricedSet := map[string]bool{}
	for _, row := range rows {
		// Same valuation path as costFromUsage: Resolve first so Codex's
		// synthetic model names map to the real model of that date.
		cost, priced := s.Prices().Cost(row.Model, time.UnixMilli(row.MinTS),
			row.InputTokens, row.OutputTokens, row.CacheRead, row.CacheCreation, row.Cache1h, row.Cache5m)
		if !priced {
			// The reported id, not the display name: this string is what a
			// pricing_overrides entry has to match.
			unpricedSet[row.Model] = true
		}
		key := [2]string{model.CanonicalModel(row.Model), row.Source}
		i, seen := index[key]
		if !seen {
			index[key] = len(bySource)
			bySource = append(bySource, modelSourceEntry{
				Model: key[0], Source: row.Source, Totals: row.Totals,
			})
			i = len(bySource) - 1
		} else {
			addTotals(&bySource[i].Totals, row.Totals)
		}
		if priced {
			// A variant with no price contributes tokens but no dollars, and the
			// id it was dropped for is named in `unpriced` — the alternative,
			// blanking the whole bar, would hide the spend we do know.
			if bySource[i].CostUSD == nil {
				bySource[i].CostUSD = new(float64)
			}
			*bySource[i].CostUSD += cost
		}
	}

	dailyOut := make([]modelDailyEntry, 0, len(daily))
	for _, d := range daily {
		dailyOut = append(dailyOut, modelDailyEntry{Bucket: d.Bucket, Model: d.Model, Totals: d.Totals})
	}

	unpriced := make([]string, 0, len(unpricedSet))
	for m := range unpricedSet {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)

	writeJSON(w, map[string]any{
		"days":      days,
		"top_n":     topN,
		"by_source": bySource,
		"daily":     dailyOut,
		"unpriced":  unpriced,
	})
}
