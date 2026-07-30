// Package store persists unified usage events in SQLite (pure-Go driver,
// no CGO, so the server cross-compiles to any platform as a single binary).
package store

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/suool/omnitoken/internal/model"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
	event_id TEXT PRIMARY KEY,
	ts INTEGER NOT NULL,
	device TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	account_label TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
	cache_1h_tokens INTEGER NOT NULL DEFAULT 0,
	cache_5m_tokens INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	gen_ms INTEGER NOT NULL DEFAULT 0,
	ttft_ms INTEGER NOT NULL DEFAULT 0,
	session_id TEXT NOT NULL DEFAULT '',
	cwd TEXT NOT NULL DEFAULT '',
	git_branch TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	app_version TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_device_ts ON events(device, ts);
CREATE INDEX IF NOT EXISTS idx_events_model_ts ON events(model, ts);
CREATE INDEX IF NOT EXISTS idx_events_repo_ts ON events(repo, ts);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// modernc/sqlite serializes writes; a single conn avoids lock contention.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + quotaSchema + settingsSchema + procSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateQuotaSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateEventsGenMS(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateEventsGenMS adds the gen_ms column to databases created before
// ADR-0009. CREATE TABLE IF NOT EXISTS leaves an existing table alone, so the
// column has to be added explicitly; existing rows default to 0 and are filled
// in by the next full rescan.
func migrateEventsGenMS(db *sql.DB) error {
	var sqlText string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='events'`).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return nil // fresh database; the schema already has it
	}
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "gen_ms") {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE events ADD COLUMN gen_ms INTEGER NOT NULL DEFAULT 0`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// InsertEvents is idempotent: duplicates (same event_id) are ignored, so
// agents and collectors can safely re-send anything. Returns inserted count.
func (s *Store) InsertEvents(events []model.Event, receivedAt int64) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO events
		(event_id, ts, device, source, model, provider, account_label,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 cache_1h_tokens, cache_5m_tokens, duration_ms, gen_ms, ttft_ms,
		 session_id, cwd, git_branch, repo, app_version, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	// Merging a second observation of an already-known event (ADR-0009 for
	// gen_ms, ADR-0013 for the rest). Two things make this necessary: a rescan
	// re-reads history that predates a derived column, and the proxy and the
	// log can each see one half of the same request — the log knows the repo,
	// the proxy knows the TTFT.
	//
	// The rules that keep this inside ADR-0004's idempotency guarantee:
	//
	//   - only ever FILL a column that is empty; never overwrite one that is
	//     set, so no observation can churn another's value;
	//   - no count column is on the list, so tokens and event totals cannot
	//     move no matter how many times a request is observed;
	//   - duration_ms is deliberately absent: on a log-owned row it means "gap
	//     to the previous record" (ADR-0006, F8 work time), which is a
	//     different quantity from the proxy's measured span.
	//
	// Because every guard fails once a row is complete, a repeat observation
	// matches nothing and reports no change — `inserted` keeps meaning what it
	// always meant, and dedup still shows up as 0.
	numFill, err := tx.Prepare(
		`UPDATE events SET gen_ms = CASE WHEN gen_ms = 0 AND ?1 > 0 THEN ?1 ELSE gen_ms END,
		                   ttft_ms = CASE WHEN ttft_ms = 0 AND ?2 > 0 THEN ?2 ELSE ttft_ms END
		 WHERE event_id = ?3
		   AND ((gen_ms = 0 AND ?1 > 0) OR (ttft_ms = 0 AND ?2 > 0))`)
	if err != nil {
		return 0, err
	}
	defer numFill.Close()

	// Attribution only the log channel can see. A proxy row starts with these
	// empty, and the log observation that follows fills them in.
	textFill, err := tx.Prepare(
		`UPDATE events SET session_id = CASE WHEN session_id = '' THEN ?1 ELSE session_id END,
		                   cwd        = CASE WHEN cwd        = '' THEN ?2 ELSE cwd        END,
		                   repo       = CASE WHEN repo       = '' THEN ?3 ELSE repo       END,
		                   git_branch = CASE WHEN git_branch = '' THEN ?4 ELSE git_branch END,
		                   app_version= CASE WHEN app_version= '' THEN ?5 ELSE app_version END
		 WHERE event_id = ?6
		   AND (session_id = '' OR cwd = '' OR repo = '' OR git_branch = '' OR app_version = '')`)
	if err != nil {
		return 0, err
	}
	defer textFill.Close()

	// duration_ms is the log channel's alone. Its meaning is "gap to the
	// previous log record" (ADR-0006), which F8 work time reads; the proxy's
	// measured span is a different quantity and stays out of this column. But
	// the log must still be able to fill it when the proxy created the row
	// first — otherwise proxied requests would silently drop out of work time.
	durFill, err := tx.Prepare(
		`UPDATE events SET duration_ms = ? WHERE event_id = ? AND duration_ms = 0 AND ? > 0`)
	if err != nil {
		return 0, err
	}
	defer durFill.Close()

	// The one sanctioned overwrite, and it goes one way only (ADR-0013): the
	// tool is the source of a request, the proxy is merely how it was seen.
	// The proxy almost always reports first — immediately, against a collector
	// that polls — so without this every proxied request would be filed under
	// `proxy` and the by-source history would break in half at the moment the
	// proxy was switched on.
	promote, err := tx.Prepare(
		`UPDATE events SET source = ?1 WHERE event_id = ?2 AND source = 'proxy' AND ?1 != 'proxy'`)
	if err != nil {
		return 0, err
	}
	defer promote.Close()

	inserted, filled := 0, 0
	for _, e := range events {
		if e.EventID == "" {
			continue
		}
		res, err := stmt.Exec(e.EventID, e.TS, e.Device, e.Source, e.Model, e.Provider, e.AccountLabel,
			e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens,
			e.Cache1hTokens, e.Cache5mTokens, e.DurationMS, e.GenMS, e.TTFTMS,
			e.SessionID, e.CWD, e.GitBranch, e.Repo, e.AppVersion, receivedAt)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
			continue
		}
		// Already known: contribute whatever this observation adds.
		changed := false
		if e.GenMS > 0 || e.TTFTMS > 0 {
			r, err := numFill.Exec(e.GenMS, e.TTFTMS, e.EventID)
			if err != nil {
				return inserted, err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				changed = true
			}
		}
		if e.SessionID != "" || e.CWD != "" || e.Repo != "" || e.GitBranch != "" || e.AppVersion != "" {
			r, err := textFill.Exec(e.SessionID, e.CWD, e.Repo, e.GitBranch, e.AppVersion, e.EventID)
			if err != nil {
				return inserted, err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				changed = true
			}
		}
		if e.Source != "" && e.Source != "proxy" {
			if _, err := promote.Exec(e.Source, e.EventID); err != nil {
				return inserted, err
			}
			if e.DurationMS > 0 {
				r, err := durFill.Exec(e.DurationMS, e.EventID, e.DurationMS)
				if err != nil {
					return inserted, err
				}
				if n, _ := r.RowsAffected(); n > 0 {
					changed = true
				}
			}
		}
		if changed {
			filled++
		}
	}
	if filled > 0 {
		log.Printf("store: merged a second observation into %d existing events (ADR-0013)", filled)
	}
	return inserted, tx.Commit()
}
