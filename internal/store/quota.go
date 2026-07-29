package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Quota snapshots (ADR-0007): authoritative window state reported by the
// provider. Stored as observations; queries take the LATEST per
// (device, source, limit_id, window_minutes) and never sum.

// scope MUST be part of the key: several scopes share one window length —
// Claude reports seven_day, seven_day_opus and seven_day_sonnet all at
// 10080 minutes, and Codex primary/secondary can both be weekly. Keying
// without scope silently drops all but one of them on INSERT OR IGNORE.
const quotaSchema = `
CREATE TABLE IF NOT EXISTS quota_snapshots (
	device TEXT NOT NULL,
	source TEXT NOT NULL,
	limit_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL DEFAULT '',
	window_minutes INTEGER NOT NULL DEFAULT 0,
	used_percent REAL NOT NULL DEFAULT 0,
	resets_at INTEGER NOT NULL DEFAULT 0,
	observed_at INTEGER NOT NULL,
	plan_type TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (device, source, limit_id, scope, window_minutes, observed_at)
);
CREATE INDEX IF NOT EXISTS idx_quota_observed ON quota_snapshots(observed_at);
`

// migrateQuotaSchema rebuilds quota_snapshots when it predates scope being
// part of the key. Snapshots are re-observable state (the next poll or scan
// repopulates them), so dropping is safe and simpler than a copy migration.
func migrateQuotaSchema(db *sql.DB) error {
	var sqlText string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='quota_snapshots'`).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return nil // fresh database
	}
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "PRIMARY KEY (device, source, limit_id, scope,") {
		return nil
	}
	if _, err := db.Exec(`DROP TABLE quota_snapshots`); err != nil {
		return err
	}
	_, err = db.Exec(quotaSchema)
	return err
}

// InsertQuotas is idempotent on the snapshot primary key.
func (s *Store) InsertQuotas(qs []model.QuotaSnapshot) (int, error) {
	if len(qs) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO quota_snapshots
		(device, source, limit_id, scope, window_minutes, used_percent, resets_at, observed_at, plan_type)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, q := range qs {
		res, err := stmt.Exec(q.Device, q.Source, q.LimitID, q.Scope, q.WindowMinutes,
			q.UsedPercent, q.ResetsAt, q.ObservedAt, q.PlanType)
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

// LatestQuotas returns the newest observation per (device, source, scope,
// window) since a cutoff. Snapshots whose window has already reset are still
// returned; callers decide staleness from ResetsAt/ObservedAt.
//
// limit_id is deliberately NOT part of that grouping. It records which channel
// reported the number, and a window's identity does not depend on how we found
// out about it — grouping by it means a channel switch leaves the retired
// channel's last reading on screen forever beside the live one. That happened:
// after ADR-0011 moved Claude quota from the OAuth endpoint to the status line,
// every window rendered twice, once per limit_id.
//
// scope stays in the grouping. Collapsing it is the ADR-0007 bug where
// per-model weekly quota (seven_day:<model>) was silently dropped.
func (s *Store) LatestQuotas(since time.Time) ([]model.QuotaSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT q.device, q.source, q.limit_id, q.scope, q.window_minutes,
		        q.used_percent, q.resets_at, q.observed_at, q.plan_type
		 FROM quota_snapshots q
		 JOIN (SELECT device, source, scope, window_minutes, MAX(observed_at) AS mx
		       FROM quota_snapshots WHERE observed_at >= ?
		       GROUP BY device, source, scope, window_minutes) m
		   ON q.device = m.device AND q.source = m.source
		  AND q.scope = m.scope
		  AND q.window_minutes = m.window_minutes
		  AND q.observed_at = m.mx
		 ORDER BY q.window_minutes, q.device`,
		since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.QuotaSnapshot
	for rows.Next() {
		var q model.QuotaSnapshot
		if err := rows.Scan(&q.Device, &q.Source, &q.LimitID, &q.Scope, &q.WindowMinutes,
			&q.UsedPercent, &q.ResetsAt, &q.ObservedAt, &q.PlanType); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
