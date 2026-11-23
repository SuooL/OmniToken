package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

const maxEventsExportRows = 10_000

// eventWithCost decorates a raw event with its query-time cost (ADR-0005).
// CostUSD is omitted entirely when the model has no pricing, so the frontend
// can distinguish "free/unpriced" from "$0".
type eventWithCost struct {
	model.Event
	DeviceDisplayName string   `json:"device_display_name,omitempty"`
	CostUSD           *float64 `json:"cost_usd,omitempty"`
}

// handleEvents serves GET /api/v1/events — the paginated raw-event
// drill-down (F13) and filtered CSV/JSON export. Filters:
// device/source/provider/model/repo/session, days (default 7); paging:
// limit (default 100, max 500; export up to 10000) + offset; optional
// format=csv|json triggers a filtered download.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	days := queryInt(r, "days", 7)
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	if format == "csv" || format == "json" {
		if q.Get("limit") == "" {
			limit = maxEventsExportRows
		} else if limit > maxEventsExportRows {
			limit = maxEventsExportRows
		}
		if q.Get("offset") == "" {
			offset = 0
		}
	} else if limit > 500 {
		limit = 500
	}
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
	names, err := s.deviceNames()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]eventWithCost, 0, len(events))
	for _, e := range events {
		row := eventWithCost{Event: e, DeviceDisplayName: names.name(e.Device)}
		if c, ok := s.Prices().Cost(e.Model, time.UnixMilli(e.TS), e.InputTokens, e.OutputTokens,
			e.CacheReadTokens, e.CacheCreationTokens, e.Cache1hTokens, e.Cache5mTokens); ok {
			row.CostUSD = &c
		}
		out = append(out, row)
	}
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=omnitoken-events.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"event_id", "time", "device", "device_display_name", "source", "provider", "model",
			"repo", "cwd", "session_id", "input_tokens", "output_tokens",
			"cache_read_tokens", "cache_creation_tokens", "gen_ms", "ttft_ms", "cost_usd",
		})
		const layout = "2006-01-02 15:04:05"
		for _, row := range out {
			_ = cw.Write([]string{
				row.EventID,
				time.UnixMilli(row.TS).Format(layout),
				row.Device,
				row.DeviceDisplayName,
				row.Source,
				row.Provider,
				row.Model,
				row.Repo,
				row.CWD,
				row.SessionID,
				i64(row.InputTokens),
				i64(row.OutputTokens),
				i64(row.CacheReadTokens),
				i64(row.CacheCreationTokens),
				i64(row.GenMS),
				i64(row.TTFTMS),
				usd(row.CostUSD),
			})
		}
		cw.Flush()
		return
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=omnitoken-events.json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total": total, "exported": len(out), "events": out})
		return
	}
	writeJSON(w, map[string]any{"total": total, "events": out})
}
