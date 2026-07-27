package store

import (
	"fmt"
	"time"
)

type Totals struct {
	Events        int64 `json:"events"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_tokens"`
	CacheCreation int64 `json:"cache_creation_tokens"`
	TotalTokens   int64 `json:"total_tokens"`
}

type BucketRow struct {
	Bucket string `json:"bucket"` // e.g. "2026-07-25"
	Totals
}

type BreakdownRow struct {
	Key      string `json:"key"`
	LastSeen int64  `json:"last_seen"`
	Totals
}

const sums = `COUNT(*),
	COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
	COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0),
	COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0)`

func (s *Store) Summary(from, to time.Time) (Totals, error) {
	var t Totals
	err := s.db.QueryRow(
		`SELECT `+sums+` FROM events WHERE ts >= ? AND ts < ?`,
		from.UnixMilli(), to.UnixMilli(),
	).Scan(&t.Events, &t.InputTokens, &t.OutputTokens, &t.CacheRead, &t.CacheCreation, &t.TotalTokens)
	return t, err
}

// Daily buckets events by local calendar day.
func (s *Store) Daily(from, to time.Time) ([]BucketRow, error) {
	rows, err := s.db.Query(
		`SELECT date(ts/1000, 'unixepoch', 'localtime') AS d, `+sums+`
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY d ORDER BY d`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BucketRow
	for rows.Next() {
		var r BucketRow
		if err := rows.Scan(&r.Bucket, &r.Events, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModelUsageRow carries per-(model, provider) sums with the cache-TTL split
// needed for cost computation (pricing is applied at query time, ADR-0005).
type ModelUsageRow struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Totals
	Cache1h int64 `json:"cache_1h_tokens"`
	Cache5m int64 `json:"cache_5m_tokens"`
	MinTS   int64 `json:"min_ts"`
}

func (s *Store) ModelUsage(from, to time.Time) ([]ModelUsageRow, error) {
	rows, err := s.db.Query(
		`SELECT model, provider, `+sums+`,
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0), COALESCE(MIN(ts),0)
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY model, provider`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsageRow
	for rows.Next() {
		var r ModelUsageRow
		if err := rows.Scan(&r.Model, &r.Provider, &r.Events, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens, &r.Cache1h, &r.Cache5m, &r.MinTS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

var breakdownDims = map[string]string{
	"device":   "device",
	"model":    "model",
	"source":   "source",
	"provider": "provider",
	"repo":     "repo",
	"branch":   "git_branch",
}

func (s *Store) Breakdown(dim string, from, to time.Time, limit int) ([]BreakdownRow, error) {
	col, ok := breakdownDims[dim]
	if !ok {
		return nil, fmt.Errorf("unknown breakdown dimension %q", dim)
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT `+col+` AS k, COALESCE(MAX(ts),0), `+sums+`
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY k
		 ORDER BY SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens) DESC
		 LIMIT ?`,
		from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BreakdownRow
	for rows.Next() {
		var r BreakdownRow
		if err := rows.Scan(&r.Key, &r.LastSeen, &r.Events, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
