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
	if _, err := db.Exec(schema + quotaSchema + settingsSchema); err != nil {
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

	// Backfill for gen_ms only (ADR-0009). Events stored before that field
	// existed carry 0, and re-observing them through INSERT OR IGNORE would
	// never fill it in, so a rescan would leave the whole history without
	// speed data. Rebuilding the database instead — what ADR-0006 did for
	// duration_ms — is no longer an option: N6 requires stored events to
	// outlive their source logs, which Claude Code deletes after 30 days.
	//
	// The guards are what keep this inside ADR-0004's idempotency rule. Only
	// this one derived column is ever rewritten, never a count or an identity;
	// `gen_ms = 0` means it is filled in once and not churned afterwards; and
	// `? > 0` stops an older agent that does not compute it from erasing a
	// good value. Because both guards fail for an already-backfilled row, the
	// statement matches nothing and reports no change — so `inserted` keeps
	// meaning what it always meant, and dedup still shows up as 0.
	backfill, err := tx.Prepare(
		`UPDATE events SET gen_ms = ? WHERE event_id = ? AND gen_ms = 0 AND ? > 0`)
	if err != nil {
		return 0, err
	}
	defer backfill.Close()

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
		// Already known. Fill in gen_ms if this observation has one and the
		// stored row does not.
		if e.GenMS > 0 {
			r, err := backfill.Exec(e.GenMS, e.EventID, e.GenMS)
			if err != nil {
				return inserted, err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				filled++
			}
		}
	}
	if filled > 0 {
		log.Printf("store: backfilled gen_ms on %d existing events (ADR-0009)", filled)
	}
	return inserted, tx.Commit()
}
