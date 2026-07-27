package server

import (
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// maxHeatmapDays caps the window at roughly two years — the calendar grid is
// only legible for about a year, and the cap keeps a hand-crafted `days=99999`
// from scanning the whole table.
const maxHeatmapDays = 731

// handleHeatmap answers GET /api/v1/heatmap?days=365 for the overview page's
// GitHub-style activity calendar (F20/GAP-2).
//
// The window is day-aligned like /api/v1/overview: it starts at local midnight
// `days-1` days ago and ends an hour into the future, so a clock skewed
// slightly ahead on a reporting device still lands inside today's cell. Days
// without activity are omitted; the client draws the empty cells. `max_tokens`
// ships alongside so a caller can bin without a second pass over the array.
func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 365)
	if days > maxHeatmapDays {
		days = maxHeatmapDays
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	rows, err := s.store.HeatmapDays(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []store.HeatmapDay{} // JSON "days": [] rather than null
	}
	var max int64
	for _, d := range rows {
		if d.Tokens > max {
			max = d.Tokens
		}
	}

	writeJSON(w, map[string]any{
		"days":         rows,
		"max_tokens":   max,
		"range_days":   days,
		"range_start":  rangeStart.Format("2006-01-02"),
		"generated_at": now.UnixMilli(),
	})
}
