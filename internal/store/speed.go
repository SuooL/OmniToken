package store

import "time"

// SpeedModelRow carries per-model generation-speed statistics (F15).
// Speed of one event = output_tokens * 1000 / duration_ms (tok/s).
//
// Semantics depend on the source channel (ADR-0006): for log sources
// (claude-code/codex) duration_ms is the gap to the previous session event —
// an approximation of generation time; for source='proxy' it is measured
// exactly and ttft_ms is populated. The two channels are never mixed: callers
// pick one via the exact flag and must label results accordingly.
type SpeedModelRow struct {
	Model        string  `json:"model"`
	Samples      int64   `json:"samples"`
	OutputTokens int64   `json:"output_tokens"`
	AvgTPS       float64 `json:"avg_tps"`
	MedianTPS    float64 `json:"median_tps"`
	P90TPS       float64 `json:"p90_tps"`
	MinTPS       float64 `json:"min_tps"`
	MaxTPS       float64 `json:"max_tps"`
	// TTFT stats are meaningful only for exact=true (logs have ttft_ms=0).
	AvgTTFTMS    float64 `json:"avg_ttft_ms"`
	MedianTTFTMS float64 `json:"median_ttft_ms"`
}

// SpeedByModel aggregates per-event speeds per model over [from, to).
// exact=true reads only source='proxy' (measured duration + TTFT);
// exact=false reads only claude-code (approximate).
//
// Codex is deliberately EXCLUDED from the approximate channel: its
// token_count lines are written right after a turn finishes, so the gap to
// the previous line is a logging artifact, not generation time (measured
// median gap 30ms vs 10.5s for claude-code), which yields speeds in the
// 10^5 tok/s range. Codex speed therefore requires the proxy channel.
//
// Events with duration_ms <= 0 or output_tokens < 8 are excluded: tiny
// outputs make the per-event speed dominated by noise, and duration 0 means
// "unknown" (batch-first events, ADR-0006).
//
// Quantiles are computed in SQL with window functions: the median averages
// the two middle ranks (exact for odd and even n), P90 uses the nearest-rank
// method (value at rank ceil(0.9*n)). TTFT is ranked independently of speed.
func (s *Store) SpeedByModel(from, to time.Time, exact bool) ([]SpeedModelRow, error) {
	srcCond := `source = 'claude-code'`
	if exact {
		srcCond = `source = 'proxy'`
	}
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
