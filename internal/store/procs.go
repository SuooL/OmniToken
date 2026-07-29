package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Live agent processes (ADR-0012): ground truth for "is a session open right
// now", as opposed to the events-based guess in live.go (ActiveSessions), which
// cannot tell an open-but-thinking session from one closed three minutes ago.
//
// Deliberately not in `events`. Process state is instantaneous, has no
// event_id (PIDs are reused), and the right response to seeing the same PID
// twice is to overwrite, not to deduplicate — all three contradict ADR-0004.
//
// live_reports carries a row per reporting device, written even when that
// device has nothing running. Without it, "no agent CLI is open on this
// machine" and "this machine never reports" are the same empty result, and the
// panel would have to show SSH-pulled hosts (which run no agent) as idle.
const procSchema = `
CREATE TABLE IF NOT EXISTS live_sessions (
	device TEXT NOT NULL,
	pid INTEGER NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL DEFAULT 0,
	observed_at INTEGER NOT NULL,
	PRIMARY KEY (device, pid)
);
CREATE INDEX IF NOT EXISTS idx_live_sessions_observed ON live_sessions(observed_at);
CREATE TABLE IF NOT EXISTS live_reports (
	device TEXT PRIMARY KEY,
	observed_at INTEGER NOT NULL
);
`

// RunningSession is one agent CLI seen running on a device.
type RunningSession struct {
	Device     string `json:"device"`
	Source     string `json:"source"`
	PID        int    `json:"pid"`
	StartedAt  int64  `json:"started_at"`  // unix ms; 0 = platform did not report
	ObservedAt int64  `json:"observed_at"` // unix ms
}

// ProcReporter is a device that reports process state at all, with the time of
// its last report — including reports that listed nothing.
type ProcReporter struct {
	Device     string `json:"device"`
	ObservedAt int64  `json:"observed_at"`
}

// ApplyProcReport replaces a device's process state with one complete report.
//
// A report is a snapshot, so applying it means the device's rows afterwards are
// exactly what the report listed: rows are overwritten by (device, pid) and
// anything left behind from an earlier report is dropped. Note this only ever
// touches the reporting device's own rows — a machine that has gone offline is
// nobody else's business to clean up, and readers age those out by TTL instead.
//
// Out-of-order reports are ignored. A delayed retry carries an older process
// list, and applying it would resurrect sessions that have since exited.
//
// The returned flag says whether the set of running PIDs actually changed —
// a session started or ended. Reports arrive on the collection interval, so
// broadcasting every one of them would push an SSE frame every 15 seconds per
// device saying nothing new; the panel keeps uptimes ticking on its own clock.
func (s *Store) ApplyProcReport(r model.ProcReport) (bool, error) {
	if r.Device == "" || r.ObservedAt == 0 {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var prev int64
	err = tx.QueryRow(`SELECT observed_at FROM live_reports WHERE device = ?`, r.Device).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if prev > r.ObservedAt {
		return false, nil
	}

	known := map[int]bool{}
	rows, err := tx.Query(`SELECT pid FROM live_sessions WHERE device = ?`, r.Device)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return false, err
		}
		known[pid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	changed := len(known) != len(r.Sessions)

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO live_sessions
		(device, pid, source, started_at, observed_at) VALUES (?,?,?,?,?)`)
	if err != nil {
		return false, err
	}
	defer stmt.Close()
	for _, ps := range r.Sessions {
		if !known[ps.PID] {
			changed = true
		}
		if _, err := stmt.Exec(r.Device, ps.PID, ps.Source, ps.StartedAt, r.ObservedAt); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(
		`DELETE FROM live_sessions WHERE device = ? AND observed_at < ?`,
		r.Device, r.ObservedAt); err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO live_reports (device, observed_at) VALUES (?,?)`,
		r.Device, r.ObservedAt); err != nil {
		return false, err
	}
	return changed, tx.Commit()
}

// RunningSessions lists processes last seen at or after the cutoff, oldest
// first. The cutoff is what makes an offline device's rows disappear without
// anyone deleting them.
func (s *Store) RunningSessions(since time.Time) ([]RunningSession, error) {
	rows, err := s.db.Query(
		`SELECT device, source, pid, started_at, observed_at
		 FROM live_sessions WHERE observed_at >= ?
		 ORDER BY started_at, device, pid`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunningSession{}
	for rows.Next() {
		var rs RunningSession
		if err := rows.Scan(&rs.Device, &rs.Source, &rs.PID, &rs.StartedAt, &rs.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}

// ProcReporters lists devices whose process state is fresh enough to trust.
// A device missing from this list has no process data — not zero sessions.
func (s *Store) ProcReporters(since time.Time) ([]ProcReporter, error) {
	rows, err := s.db.Query(
		`SELECT device, observed_at FROM live_reports WHERE observed_at >= ? ORDER BY device`,
		since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProcReporter{}
	for rows.Next() {
		var p ProcReporter
		if err := rows.Scan(&p.Device, &p.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
