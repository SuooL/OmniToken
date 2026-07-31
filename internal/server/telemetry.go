package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

const telemetryResponseLimit = 512 * 1024

type telemetrySpeedResponse struct {
	Range             string                          `json:"range"`
	StartMS           int64                           `json:"start_ms"`
	EndMS             int64                           `json:"end_ms"`
	BucketMS          int64                           `json:"bucket_ms"`
	MeasuredSources   []string                        `json:"measured_sources"`
	UnmeasuredSources []string                        `json:"unmeasured_sources"`
	Coverage          []store.TelemetrySourceCoverage `json:"coverage"`
	Series            []store.TelemetryBucket         `json:"series"`
	Aggregate10MTPS   float64                         `json:"aggregate_10m_tps"`
	PeakTPS           float64                         `json:"peak_tps"`
	PeakAt            int64                           `json:"peak_at"`
	ActiveRatio       float64                         `json:"active_ratio"`
}

type telemetryResponse struct {
	GeneratedAt int64                       `json:"generated_at"`
	Timezone    string                      `json:"timezone"`
	Today       store.TelemetryTodayUsage   `json:"today"`
	Rolling5H   store.TelemetryRollingUsage `json:"rolling_5h"`
	Speed       telemetrySpeedResponse      `json:"speed"`
}

type telemetryRange struct {
	duration time.Duration
	bucket   time.Duration
}

var telemetryRanges = map[string]telemetryRange{
	"1h":  {duration: time.Hour, bucket: time.Minute},
	"5h":  {duration: 5 * time.Hour, bucket: 5 * time.Minute},
	"24h": {duration: 24 * time.Hour, bucket: 30 * time.Minute},
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "5h"
	}
	selected, ok := telemetryRanges[rangeName]
	if !ok {
		http.Error(w, "range must be one of 1h, 5h, 24h", http.StatusBadRequest)
		return
	}

	now := s.currentTime()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	usage, err := s.store.TelemetryUsage(todayStart, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	speed, err := s.store.TelemetrySpeedSeries(now.Add(-selected.duration), now, selected.bucket)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tenMinute, err := s.store.TelemetrySpeedSeries(now.Add(-10*time.Minute), now, 10*time.Minute)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	speedResponse := telemetrySpeedResponse{
		Range:             rangeName,
		StartMS:           speed.StartMS,
		EndMS:             speed.EndMS,
		BucketMS:          speed.BucketMS,
		MeasuredSources:   speed.MeasuredSources,
		UnmeasuredSources: speed.UnmeasuredSources,
		Coverage:          speed.Coverage,
		Series:            speed.Series,
	}
	if len(tenMinute.Series) > 0 {
		speedResponse.Aggregate10MTPS = tenMinute.Series[0].AggregateTPS
	}
	var totalActiveMS int64
	for _, bucket := range speed.Series {
		totalActiveMS += bucket.ActiveMS
		if bucket.AggregateTPS > speedResponse.PeakTPS {
			speedResponse.PeakTPS = bucket.AggregateTPS
			speedResponse.PeakAt = bucket.StartMS
		}
	}
	if durationMS := speed.EndMS - speed.StartMS; durationMS > 0 {
		speedResponse.ActiveRatio = float64(totalActiveMS) / float64(durationMS)
	}

	resp := telemetryResponse{
		GeneratedAt: now.UnixMilli(),
		Timezone:    now.Location().String(),
		Today:       usage.Today,
		Rolling5H:   usage.Rolling5H,
		Speed:       speedResponse,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(data) >= telemetryResponseLimit {
		http.Error(w,
			fmt.Sprintf("telemetry response exceeds %d byte limit", telemetryResponseLimit),
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
