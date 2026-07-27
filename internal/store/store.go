// Package store persists unified usage events in SQLite (pure-Go driver,
// no CGO, so the server cross-compiles to any platform as a single binary).
package store

import (
	"database/sql"
	"os"
	"path/filepath"

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
	return &Store{db: db}, nil
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
		 cache_1h_tokens, cache_5m_tokens, duration_ms, ttft_ms,
		 session_id, cwd, git_branch, repo, app_version, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, e := range events {
		if e.EventID == "" {
			continue
		}
		res, err := stmt.Exec(e.EventID, e.TS, e.Device, e.Source, e.Model, e.Provider, e.AccountLabel,
			e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens,
			e.Cache1hTokens, e.Cache5mTokens, e.DurationMS, e.TTFTMS,
			e.SessionID, e.CWD, e.GitBranch, e.Repo, e.AppVersion, receivedAt)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, tx.Commit()
}
