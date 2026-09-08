package store

import (
	"fmt"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// periodExpr is the SQLite expression naming a row's bucket. daily matches
// Daily's date() bucketing; weekly is "%Y-W%W" (Monday-first week number),
// monthly "%Y-%m" — all in localtime, so a bucket boundary means the same here
// as everywhere else (ADR-0021).
func periodExpr(granularity string) (string, error) {
	switch granularity {
	case "daily":
		return `date(ts/1000, 'unixepoch', 'localtime')`, nil
	case "weekly":
		return `strftime('%Y-W%W', ts/1000, 'unixepoch', 'localtime')`, nil
	case "monthly":
		return `strftime('%Y-%m', ts/1000, 'unixepoch', 'localtime')`, nil
	}
	return "", fmt.Errorf("unknown granularity %q", granularity)
}

// PeriodRows buckets events by local calendar period (F12 reports).
func (s *Store) PeriodRows(granularity string, from, to time.Time) ([]BucketRow, error) {
	if granularity == "daily" {
		return s.Daily(from, to)
	}
	expr, err := periodExpr(granularity)
	if err != nil {
		return nil, err
	}
	rows, err := s.rdb.Query(
		`SELECT `+expr+` AS b, `+sums+`
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY b ORDER BY b`,
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

// PeriodModelRow is one (bucket, model, provider) aggregate.
//
// It exists because a price applies to a model and a report row does not: a day
// mixes Opus with Haiku at a 25x rate difference, so a bucket's cost can only
// be reached by pricing its models one at a time and adding the results. The
// same reason the overview splits by model before valuing anything (ADR-0005).
type PeriodModelRow struct {
	Bucket string `json:"bucket"`
	ModelUsageRow
}

// PeriodModelUsage returns the per-model split of every bucket PeriodRows
// returns, over the same range and with the same bucketing.
func (s *Store) PeriodModelUsage(granularity string, from, to time.Time) ([]PeriodModelRow, error) {
	expr, err := periodExpr(granularity)
	if err != nil {
		return nil, err
	}
	rows, err := s.rdb.Query(
		`SELECT `+expr+` AS b, model, provider, `+sums+`,
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0),
		        COALESCE(MIN(ts),0)
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY b, model, provider`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeriodModelRow
	for rows.Next() {
		var r PeriodModelRow
		if err := rows.Scan(&r.Bucket, &r.Model, &r.Provider, &r.Events,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens,
			&r.Cache1h, &r.Cache5m, &r.MinTS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionModelRow is one (session, device, model, provider) aggregate.
type SessionModelRow struct {
	SessionID string `json:"session_id"`
	Device    string `json:"device"`
	ModelUsageRow
}

// SessionModelUsage returns the per-model split of exactly the sessions
// SessionRows would return for the same arguments.
//
// The inner SELECT repeats SessionRows' selection rather than taking a list of
// keys from the caller, so the two cannot drift into pricing a different set of
// sessions than the one on screen.
func (s *Store) SessionModelUsage(from, to time.Time, limit int) ([]SessionModelRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.rdb.Query(
		`WITH shown AS (
		   SELECT session_id, device FROM events WHERE ts >= ? AND ts < ?
		   GROUP BY session_id, device ORDER BY MAX(ts) DESC LIMIT ?
		 )
		 SELECT e.session_id, e.device, e.model, e.provider, `+sums+`,
		        COALESCE(SUM(e.cache_1h_tokens),0), COALESCE(SUM(e.cache_5m_tokens),0),
		        COALESCE(MIN(e.ts),0)
		 FROM events e JOIN shown ON e.session_id = shown.session_id AND e.device = shown.device
		 WHERE e.ts >= ? AND e.ts < ?
		 GROUP BY e.session_id, e.device, e.model, e.provider`,
		from.UnixMilli(), to.UnixMilli(), limit, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionModelRow
	for rows.Next() {
		var r SessionModelRow
		if err := rows.Scan(&r.SessionID, &r.Device, &r.Model, &r.Provider, &r.Events,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens,
			&r.Cache1h, &r.Cache5m, &r.MinTS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionRow aggregates one tool session on one device. Repo/Model/Source
// are MAX() picks — a session normally has a single value for each; MAX just
// resolves the odd mixed case deterministically.
type SessionRow struct {
	SessionID string `json:"session_id"`
	Device    string `json:"device"`
	Source    string `json:"source"`
	Repo      string `json:"repo"`
	Model     string `json:"model"`
	FirstTS   int64  `json:"first_ts"`
	LastTS    int64  `json:"last_ts"`
	Totals
}

// SessionRows returns per-(session, device) aggregates, most recent first.
func (s *Store) SessionRows(from, to time.Time, limit int) ([]SessionRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.rdb.Query(
		`SELECT session_id, device, MAX(source), MAX(repo), MAX(model),
		        COALESCE(MIN(ts),0), COALESCE(MAX(ts),0), `+sums+`
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY session_id, device
		 ORDER BY MAX(ts) DESC
		 LIMIT ?`,
		from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.SessionID, &r.Device, &r.Source, &r.Repo, &r.Model,
			&r.FirstTS, &r.LastTS,
			&r.Events, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens); err != nil {
			return nil, err
		}
		// One sampled model name per session, shown as a label — so it wears the
		// display name like every other view (internal/model/canonical.go).
		r.Model = model.CanonicalModel(r.Model)
		out = append(out, r)
	}
	return out, rows.Err()
}
