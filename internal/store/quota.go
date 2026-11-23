package store

import (
	"database/sql"
	"log"
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
CREATE TABLE IF NOT EXISTS quota_latest (
	device TEXT NOT NULL,
	source TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT '',
	window_minutes INTEGER NOT NULL DEFAULT 0,
	limit_id TEXT NOT NULL DEFAULT '',
	used_percent REAL NOT NULL DEFAULT 0,
	resets_at INTEGER NOT NULL DEFAULT 0,
	observed_at INTEGER NOT NULL,
	plan_type TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (device, source, scope, window_minutes)
);
CREATE INDEX IF NOT EXISTS idx_quota_latest_observed ON quota_latest(observed_at);
`

// migrateQuotaLatest populates quota_latest from existing quota_snapshots once
// on upgrade so LatestQuotas serves in O(active scopes) without scanning the
// historical snapshot table.
func migrateQuotaLatest(db *sql.DB) error {
	var have int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM quota_latest)`).Scan(&have); err != nil {
		return err
	}
	if have == 1 {
		return nil
	}
	_, err := db.Exec(`
		INSERT OR REPLACE INTO quota_latest
			(device, source, scope, window_minutes, limit_id, used_percent, resets_at, observed_at, plan_type)
		SELECT device, source, scope, window_minutes, limit_id, used_percent, resets_at, observed_at, plan_type
		FROM (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY device, source, scope, window_minutes
				ORDER BY resets_at / 3600000 DESC, observed_at DESC
			) AS rn
			FROM quota_snapshots
		)
		WHERE rn = 1`)
	return err
}

func insertQuotasTx(tx *sql.Tx, qs []model.QuotaSnapshot) (int, []model.QuotaSnapshot, error) {
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO quota_snapshots
		(device, source, limit_id, scope, window_minutes, used_percent, resets_at, observed_at, plan_type)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, nil, err
	}
	defer stmt.Close()
	latestStmt, err := tx.Prepare(`INSERT INTO quota_latest
		(device, source, scope, window_minutes, limit_id, used_percent, resets_at, observed_at, plan_type)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(device, source, scope, window_minutes) DO UPDATE SET
			limit_id = excluded.limit_id,
			used_percent = excluded.used_percent,
			resets_at = excluded.resets_at,
			observed_at = excluded.observed_at,
			plan_type = excluded.plan_type
		WHERE (excluded.resets_at / 3600000 > quota_latest.resets_at / 3600000)
		   OR (excluded.resets_at / 3600000 = quota_latest.resets_at / 3600000
		       AND excluded.observed_at > quota_latest.observed_at)`)
	if err != nil {
		return 0, nil, err
	}
	defer latestStmt.Close()

	n := 0
	var fresh []model.QuotaSnapshot
	for _, q := range qs {
		res, err := stmt.Exec(q.Device, q.Source, q.LimitID, q.Scope, q.WindowMinutes,
			q.UsedPercent, q.ResetsAt, q.ObservedAt, q.PlanType)
		if err != nil {
			return 0, nil, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
			fresh = append(fresh, q)
			if _, err := latestStmt.Exec(q.Device, q.Source, q.Scope, q.WindowMinutes,
				q.LimitID, q.UsedPercent, q.ResetsAt, q.ObservedAt, q.PlanType); err != nil {
				return 0, nil, err
			}
		}
	}
	return n, fresh, nil
}

func refreshQuotaLatestForDeviceTx(tx *sql.Tx, device string) error {
	if _, err := tx.Exec(`DELETE FROM quota_latest WHERE device = ?`, device); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT OR REPLACE INTO quota_latest
			(device, source, scope, window_minutes, limit_id, used_percent, resets_at, observed_at, plan_type)
		SELECT device, source, scope, window_minutes, limit_id, used_percent, resets_at, observed_at, plan_type
		FROM (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY device, source, scope, window_minutes
				ORDER BY resets_at / 3600000 DESC, observed_at DESC
			) AS rn
			FROM quota_snapshots
			WHERE device = ?
		)
		WHERE rn = 1`, device)
	return err
}

func (s *Store) observeCapacityBatch(fresh []model.QuotaSnapshot, now int64) {
	for _, q := range fresh {
		if err := s.ObserveCapacityFromQuota(q, now); err != nil {
			log.Printf("quota[capacity]: observe %s/%s/%dm: %v", q.Source, q.Scope, q.WindowMinutes, err)
		}
	}
}

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
	n, fresh, err := insertQuotasTx(tx, qs)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// The calibration follows the snapshot, on every arrival path (ADR-0034),
	// and after the commit so the token scan never runs inside a write
	// transaction. A failure is logged rather than returned: the snapshot itself
	// is stored and correct, and a missed sample only leaves an estimate — one
	// designed to be absent until it has evidence — slightly less sharp.
	s.observeCapacityBatch(fresh, time.Now().UnixMilli())
	return n, nil
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
//
// Windows are compared at hour resolution because a boundary can be derived
// rather than stated: Codex reports "resets in N seconds", so every observation
// computes a slightly different instant. Comparing raw milliseconds read that
// jitter as a newer window and let a stale reading win by being 5ms "later".
// Minute resolution was the first fix, on the assumption the jitter stayed
// sub-minute — but on real data one weekly window's resets_at spanned 18
// seconds (…933s … 951s) straddling a minute boundary, so the readings split
// into two minutes and a three-day-old 10% look (in the later minute) outranked
// a minute-old 48% one, freezing the panel's Codex quota days behind. The gap
// between real windows is the window length itself — at least 5h (the smallest,
// five_hour) — so an hour is still far finer than that, and comfortably coarser
// than any jitter seen. See TestLatestQuotasIgnoresMultiSecondJitterAcross...
func (s *Store) LatestQuotas(since time.Time) ([]model.QuotaSnapshot, error) {
	rows, err := s.rdb.Query(
		`SELECT device, source, limit_id, scope, window_minutes,
		        used_percent, resets_at, observed_at, plan_type
		 FROM quota_latest
		 WHERE observed_at >= ?
		 ORDER BY window_minutes, device, source, scope`,
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
