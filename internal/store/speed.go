package store

import "time"

// SpeedModelRow carries per-model statistics for the measured channel (F15):
// the local proxy, where duration_ms brackets the request itself and ttft_ms is
// the real first-token latency. Speed of one event = output_tokens * 1000 /
// duration_ms (tok/s).
type SpeedModelRow struct {
	Model        string  `json:"model"`
	Samples      int64   `json:"samples"`
	OutputTokens int64   `json:"output_tokens"`
	AvgTPS       float64 `json:"avg_tps"`
	MedianTPS    float64 `json:"median_tps"`
	P90TPS       float64 `json:"p90_tps"`
	MinTPS       float64 `json:"min_tps"`
	MaxTPS       float64 `json:"max_tps"`
	AvgTTFTMS    float64 `json:"avg_ttft_ms"`
	MedianTTFTMS float64 `json:"median_ttft_ms"`
}

// ProxySpeedByModel aggregates per-event speeds per model over [from, to) for
// the proxy channel only (source='proxy'), where duration_ms is measured around
// the request and ttft_ms is real.
//
// The log channel used to be computed here too, from duration_ms — the gap to
// the previous session event. ADR-0009 took it apart on real samples (17% of
// events had gaps over 30s, holding human thinking and tool runs) and it is
// gone: log-derived speed now comes from gen_ms on the union basis, in
// SpeedByModelUnion. Comparing the two on this machine's 30 days before
// deleting it, as that ADR required: claude-opus-4-8 read 137.4 tok/s as a mean
// of per-event ratios and 31.0 summed over duration_ms, against 68.3 on the
// generation interval — the old channel was wrong in both directions at once.
//
// Events with duration_ms <= 0 or output_tokens < 8 are excluded: tiny outputs
// make the per-event speed dominated by noise, and duration 0 means "unknown".
//
// Quantiles are computed in SQL with window functions: the median averages
// the two middle ranks (exact for odd and even n), P90 uses the nearest-rank
// method (value at rank ceil(0.9*n)). TTFT is ranked independently of speed.
func (s *Store) ProxySpeedByModel(from, to time.Time) ([]SpeedModelRow, error) {
	const srcCond = `source = 'proxy'`
	rows, err := s.db.Query(
		`WITH sp AS (
		   SELECT model,
		          output_tokens * 1000.0 / duration_ms AS tps,
		          ttft_ms, output_tokens
		   FROM events
		   WHERE ts >= ? AND ts < ?
		     AND duration_ms > 0 AND output_tokens >= 8
		     AND `+srcCond+`
		 ), ranked AS (
		   SELECT model, tps, ttft_ms, output_tokens,
		          ROW_NUMBER() OVER (PARTITION BY model ORDER BY tps)     AS rn,
		          ROW_NUMBER() OVER (PARTITION BY model ORDER BY ttft_ms) AS trn,
		          COUNT(*)    OVER (PARTITION BY model)                   AS n
		   FROM sp
		 )
		 SELECT model, MAX(n), COALESCE(SUM(output_tokens),0),
		        COALESCE(AVG(tps),0), COALESCE(MIN(tps),0), COALESCE(MAX(tps),0),
		        COALESCE(AVG(CASE WHEN rn IN ((n+1)/2, (n+2)/2) THEN tps END),0),
		        COALESCE(MAX(CASE WHEN rn = (9*n+9)/10 THEN tps END),0),
		        COALESCE(AVG(ttft_ms),0),
		        COALESCE(AVG(CASE WHEN trn IN ((n+1)/2, (n+2)/2) THEN ttft_ms END),0)
		 FROM ranked
		 GROUP BY model
		 ORDER BY SUM(output_tokens) DESC`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpeedModelRow
	for rows.Next() {
		var r SpeedModelRow
		if err := rows.Scan(&r.Model, &r.Samples, &r.OutputTokens,
			&r.AvgTPS, &r.MinTPS, &r.MaxTPS, &r.MedianTPS, &r.P90TPS,
			&r.AvgTTFTMS, &r.MedianTTFTMS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
