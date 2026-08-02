package store

import (
	"fmt"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// PeriodRows buckets events by local calendar period (F12 reports).
// daily reuses Daily; weekly buckets are "%Y-W%W" (Monday-first week
// number), monthly "%Y-%m" — all computed by SQLite strftime in localtime,
// consistent with Daily's date() bucketing.
func (s *Store) PeriodRows(granularity string, from, to time.Time) ([]BucketRow, error) {
	var expr string
	switch granularity {
	case "daily":
		return s.Daily(from, to)
	case "weekly":
		expr = `strftime('%Y-W%W', ts/1000, 'unixepoch', 'localtime')`
	case "monthly":
		expr = `strftime('%Y-%m', ts/1000, 'unixepoch', 'localtime')`
	default:
		return nil, fmt.Errorf("unknown granularity %q", granularity)
	}
	rows, err := s.db.Query(
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
	rows, err := s.db.Query(
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
