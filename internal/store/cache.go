package store

import "time"

// CacheModelRow carries per-model cache aggregates (F16). MinTS feeds
// pricing.Resolve so synthetic Codex model names map to the model of that
// date, mirroring costFromUsage.
type CacheModelRow struct {
	Model         string `json:"model"`
	Events        int64  `json:"events"`
	Input         int64  `json:"input_tokens"`
	CacheRead     int64  `json:"cache_read_tokens"`
	CacheCreation int64  `json:"cache_creation_tokens"`
	Cache1h       int64  `json:"cache_1h_tokens"`
	Cache5m       int64  `json:"cache_5m_tokens"`
	MinTS         int64  `json:"min_ts"`
}

// CacheDailyRow carries per-local-day input vs cache-read volume, the two
// terms of the cache hit rate.
type CacheDailyRow struct {
	Bucket    string `json:"bucket"` // e.g. "2026-07-25"
	Input     int64  `json:"input_tokens"`
	CacheRead int64  `json:"cache_read_tokens"`
}

// CacheByModel aggregates cache-relevant token sums per model, largest
// cache traffic first.
func (s *Store) CacheByModel(from, to time.Time) ([]CacheModelRow, error) {
	rows, err := s.db.Query(
		`SELECT model, COUNT(*),
		        COALESCE(SUM(input_tokens),0),
		        COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0),
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0),
		        COALESCE(MIN(ts),0)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY model
		 ORDER BY SUM(input_tokens+cache_read_tokens+cache_creation_tokens) DESC`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheModelRow
	for rows.Next() {
		var r CacheModelRow
		if err := rows.Scan(&r.Model, &r.Events, &r.Input, &r.CacheRead, &r.CacheCreation, &r.Cache1h, &r.Cache5m, &r.MinTS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CacheDaily buckets input and cache-read tokens by local calendar day.
func (s *Store) CacheDaily(from, to time.Time) ([]CacheDailyRow, error) {
	rows, err := s.db.Query(
		`SELECT date(ts/1000, 'unixepoch', 'localtime') AS d,
		        COALESCE(SUM(input_tokens),0), COALESCE(SUM(cache_read_tokens),0)
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY d ORDER BY d`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheDailyRow
	for rows.Next() {
		var r CacheDailyRow
		if err := rows.Scan(&r.Bucket, &r.Input, &r.CacheRead); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
