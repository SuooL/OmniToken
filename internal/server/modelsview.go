package server

import (
	"net/http"
	"sort"
	"time"

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

	bySource := make([]modelSourceEntry, 0, len(rows))
	unpricedSet := map[string]bool{}
	for _, row := range rows {
		e := modelSourceEntry{Model: row.Model, Source: row.Source, Totals: row.Totals}
		// Same valuation path as costFromUsage: Resolve first so Codex's
		// synthetic model names map to the real model of that date.
		cost, ok := s.Prices().Cost(row.Model, time.UnixMilli(row.MinTS),
			row.InputTokens, row.OutputTokens, row.CacheRead, row.CacheCreation, row.Cache1h, row.Cache5m)
		if ok {
			e.CostUSD = &cost
		} else {
			unpricedSet[row.Model] = true
		}
		bySource = append(bySource, e)
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
