package server

import (
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// handleSpeed answers GET /api/v1/speed?days=30 (F15): per-model tokens/sec
// distribution and TTFT. The two channels are computed separately and never
// mixed (docs/requirements.md F15): "approx" comes from log sources where
// duration_ms is the gap to the previous session event (ADR-0006), "exact"
// from the local proxy (source='proxy') with measured duration and TTFT.
func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	approx, err := s.store.SpeedByModel(rangeStart, end, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	exact, err := s.store.SpeedByModel(rangeStart, end, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if approx == nil {
		approx = []store.SpeedModelRow{}
	}
	if exact == nil {
		exact = []store.SpeedModelRow{}
	}
	writeJSON(w, map[string]any{
		"days":      days,
		"approx":    approx,
		"exact":     exact,
		"has_exact": len(exact) > 0,
	})
}
