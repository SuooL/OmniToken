package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Quota snapshots (ADR-0007): authoritative window state reported by the
// provider. Stored as observations; queries take one reading per
// (device, source, scope, window_minutes) — newest window first, then newest
// look within it (see LatestQuotas) — and never sum.

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
// LatestQuotas takes one reading per (device, source, scope, window): the one
// about the newest window, and within that window the newest look.
//
// The order of those two keys is the whole point. `resets_at` says *which
// window* a reading is about; `observed_at` only says when someone looked. A
// machine running several Claude Code sessions has several status-line hooks,
// and a long-lived session keeps reporting the account state it captured hours
// ago — a 5h window that has since reset. That reading is observed *now*, so
// ordering by observed_at alone let it win; the live view then dropped it for
// having already reset (correctly), and the panel showed no 5h quota at all
// until the next session reported. The two alternated every couple of seconds.
//
// Readings with no boundary (resets_at = 0) sort last, so a source that does
// report one is preferred — and when nothing reports one, the rule falls back
// to time on its own.
func (s *Store) LatestQuotas(since time.Time) ([]model.QuotaSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT device, source, limit_id, scope, window_minutes,
		        used_percent, resets_at, observed_at, plan_type
		 FROM (SELECT *, ROW_NUMBER() OVER (
		         PARTITION BY device, source, scope, window_minutes
		         ORDER BY resets_at DESC, observed_at DESC) AS rn
		       FROM quota_snapshots WHERE observed_at >= ?)
		 WHERE rn = 1
		 ORDER BY window_minutes, device`,
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
