package server

import (
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

const (
	// The live curve's span and resolution. An hour at one-minute resolution
	// shows the shape of a working session — long enough to hold several turns,
	// fine enough that a single response is still a visible feature.
	speedSeriesWindow = time.Hour
	speedSeriesBucket = time.Minute
)

// handleSpeed answers GET /api/v1/speed?days=30 (F15).
//
// Three separate things, never averaged together:
//
//   - series: the last hour of generation speed, bucketed — the realtime view;
//   - models: per-model speed from log-derived generation intervals, on the
//     union basis (ADR-0009), with the coverage each figure rests on;
//   - exact: the local proxy channel, which measures duration and TTFT directly.
//
// Codex appears in none of them. Its rollout files replay history with the
// flush timestamp rather than the event's own (70% of records), so it has no
// usable generation interval — stated on the page rather than left as a silent
// gap (ADR-0009).
func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	models, err := s.store.SpeedByModelUnion(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	exact, err := s.store.ProxySpeedByModel(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	series, err := s.store.SpeedSeries(now.Add(-speedSeriesWindow), now, speedSeriesBucket, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same call as the Live page's headline, so the two pages cannot report
	// different numbers for the same ten minutes.
	live, err := s.store.LiveSpeedSince(now.Add(-burnWindow), now, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if models == nil {
		models = []store.SpeedModelStat{}
	}
	if exact == nil {
		exact = []store.SpeedModelRow{}
	}

	writeJSON(w, map[string]any{
		"days":   days,
		"models": models,
		"exact":  exact,
		"series": map[string]any{
			"buckets":        series,
			"bucket_ms":      speedSeriesBucket.Milliseconds(),
			"window_minutes": int(speedSeriesWindow.Minutes()),
		},
		"live":      live,
		"has_exact": len(exact) > 0,
	})
}
