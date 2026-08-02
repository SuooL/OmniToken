package model

// Live agent processes (ADR-0012): which agent CLIs are running *right now* on
// a machine, read from its own process table.
//
// This is the third payload alongside Event and QuotaSnapshot, and the only one
// that is neither a flow nor deduplicated. A process has no event_id (PIDs get
// reused), and seeing the same PID twice means it is still alive — not that we
// counted it twice. Hence its own table, keyed by (Device, PID) and overwritten
// on every report, well away from the events table's identity rules (ADR-0004).

// ProcReport is one machine's complete view of its agent processes at a moment.
//
// The envelope exists so that "reported, nothing running" is representable at
// all: a bare list cannot distinguish it from a machine that never reports,
// which is exactly the distinction the panel has to draw for SSH-pulled hosts
// (they run no agent, so they have no process data — not zero sessions).
type ProcReport struct {
	Device     string        `json:"device"`
	ObservedAt int64         `json:"observed_at"` // unix ms
	Sessions   []ProcSession `json:"sessions"`
}

// ProcSession is one running agent CLI.
type ProcSession struct {
	PID       int    `json:"pid"`
	Source    string `json:"source"`     // claude-code | codex
	StartedAt int64  `json:"started_at"` // unix ms; 0 when the platform cannot tell
}
