package server

import (
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

// eventWithCost decorates a raw event with its query-time cost (ADR-0005).
// CostUSD is omitted entirely when the model has no pricing, so the frontend
// can distinguish "free/unpriced" from "$0".
type eventWithCost struct {
	model.Event
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// handleEvents serves GET /api/v1/events — the paginated raw-event
// drill-down (F13). Filters: device/source/provider/model/repo/session,
// days (default 7); paging: limit (default 100, max 500) + offset.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := queryInt(r, "days", 7)
	limit := queryInt(r, "limit", 100)
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	now := time.Now()
	f := store.EventFilter{
		Device:    q.Get("device"),
		Source:    q.Get("source"),
		Provider:  q.Get("provider"),
		Model:     q.Get("model"),
		Repo:      q.Get("repo"),
		SessionID: q.Get("session"),
		From:      now.AddDate(0, 0, -days),
		To:        now.Add(time.Hour),
	}
	total, err := s.store.EventCount(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := s.store.EventPage(f, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]eventWithCost, 0, len(events))
	for _, e := range events {
		row := eventWithCost{Event: e}
		if c, ok := s.Prices().Cost(e.Model, time.UnixMilli(e.TS), e.InputTokens, e.OutputTokens,
			e.CacheReadTokens, e.CacheCreationTokens, e.Cache1hTokens, e.Cache5mTokens); ok {
			row.CostUSD = &c
		}
		out = append(out, row)
	}
	writeJSON(w, map[string]any{"total": total, "events": out})
}
