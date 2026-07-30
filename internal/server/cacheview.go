package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// cacheModelEntry is one row of the cache view (F16): raw aggregates plus
// the two derived numbers the page is about.
type cacheModelEntry struct {
	Model         string  `json:"model"`
	Events        int64   `json:"events"`
	Input         int64   `json:"input_tokens"`
	CacheRead     int64   `json:"cache_read_tokens"`
	CacheCreation int64   `json:"cache_creation_tokens"`
	Cache1h       int64   `json:"cache_1h_tokens"`
	Cache5m       int64   `json:"cache_5m_tokens"`
	HitRate       float64 `json:"hit_rate"`
	SavedUSD      float64 `json:"saved_usd"`
}

type cacheDailyEntry struct {
	Bucket    string  `json:"bucket"`
	Input     int64   `json:"input_tokens"`
	CacheRead int64   `json:"cache_read_tokens"`
	HitRate   float64 `json:"hit_rate"`
}

type cacheTotals struct {
	HitRate  float64 `json:"hit_rate"`
	SavedUSD float64 `json:"saved_usd"`
	Cache1h  int64   `json:"cache_1h_tokens"`
	Cache5m  int64   `json:"cache_5m_tokens"`
}

// cacheTraffic is what the cache page ranks by: everything that either hit the
// cache or had to be sent because it did not.
func cacheTraffic(e *cacheModelEntry) int64 {
	return e.Input + e.CacheRead + e.CacheCreation
}

func hitRate(cacheRead, input int64) float64 {
	if cacheRead+input == 0 {
		return 0
	}
	return float64(cacheRead) / float64(cacheRead+input)
}

// handleCache answers GET /api/v1/cache?days=30: hit rate, dollars saved by
// cache reads, and the 1h/5m TTL write split. Savings are what the cached
// prefix would have cost at the input rate minus what reads actually cost;
// models without pricing go to "unpriced" instead of silently counting zero
// (ADR-0005).
func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	rows, err := s.store.CacheByModel(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	daily, err := s.store.CacheDaily(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Priced per reported id, displayed per folded name (internal/model/
	// canonical.go). The order matters: a price is looked up by what the tool
	// sent, while one model reaching us through two channels is one row on
	// screen. Merging after pricing gets both.
	models := []*cacheModelEntry{}
	byName := map[string]*cacheModelEntry{}
	totals := cacheTotals{}
	var totalRead, totalInput int64
	unpriced := []string{}
	for _, row := range rows {
		var saved float64
		// Resolve handles Codex synthetic model names (codex-auto-review, "").
		p, ok := s.Prices().Lookup(s.Prices().Resolve(row.Model, time.UnixMilli(row.MinTS)))
		if ok && p.CacheRead > 0 {
			saved = float64(row.CacheRead) * (p.Input - p.CacheRead)
		} else if !ok {
			// Reported id, not the display name: this list exists so the user
			// can write a pricing override that actually matches.
			unpriced = append(unpriced, row.Model)
		}
		// If ok but CacheRead price is 0, reads bill at the input rate
		// (ADR-0005 fallback) — genuinely zero savings, not unpriced.
		name := model.CanonicalModel(row.Model)
		e := byName[name]
		if e == nil {
			e = &cacheModelEntry{Model: name}
			byName[name] = e
			models = append(models, e)
		}
		e.Events += row.Events
		e.Input += row.Input
		e.CacheRead += row.CacheRead
		e.CacheCreation += row.CacheCreation
		e.Cache1h += row.Cache1h
		e.Cache5m += row.Cache5m
		e.SavedUSD += saved
		// Rates are recomputed from the merged sums, never averaged: the mean
		// of two hit rates is not the hit rate of the two together.
		e.HitRate = hitRate(e.CacheRead, e.Input)

		totals.SavedUSD += saved
		totals.Cache1h += row.Cache1h
		totals.Cache5m += row.Cache5m
		totalRead += row.CacheRead
		totalInput += row.Input
	}
	totals.HitRate = hitRate(totalRead, totalInput)

	// Re-sorted rather than kept in the store's order: merging changes the
	// ranking, since a model split across two channels can outweigh one that
	// was listed above both of its halves.
	sort.Slice(models, func(i, j int) bool {
		return cacheTraffic(models[i]) > cacheTraffic(models[j])
	})

	dailyOut := make([]cacheDailyEntry, 0, len(daily))
	for _, d := range daily {
		dailyOut = append(dailyOut, cacheDailyEntry{
			Bucket: d.Bucket, Input: d.Input, CacheRead: d.CacheRead,
			HitRate: hitRate(d.CacheRead, d.Input),
		})
	}

	writeJSON(w, map[string]any{
		"days":     days,
		"models":   models,
		"totals":   totals,
		"daily":    dailyOut,
		"unpriced": unpriced,
	})
}
