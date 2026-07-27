package store

import (
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// EventFilter narrows EventPage/EventCount. Empty string fields mean "no
// filter"; zero From/To mean an unbounded side of the time range.
type EventFilter struct {
	Device    string
	Source    string
	Provider  string
	Model     string
	Repo      string
	SessionID string
	From, To  time.Time
}

// where builds the parameterized WHERE clause. Column names are hardcoded
// here (never taken from input) so user values only ever travel as ? args.
func (f EventFilter) where() (string, []any) {
	var conds []string
	var args []any
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, f.To.UnixMilli())
	}
	for _, c := range []struct{ col, val string }{
		{"device", f.Device},
		{"source", f.Source},
		{"provider", f.Provider},
		{"model", f.Model},
		{"repo", f.Repo},
		{"session_id", f.SessionID},
	} {
		if c.val != "" {
			conds = append(conds, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if len(conds) == 0 {
		return "1=1", nil
	}
	return strings.Join(conds, " AND "), args
}

// EventPage returns one page of raw events matching the filter, newest first.
func (s *Store) EventPage(f EventFilter, limit, offset int) ([]model.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := f.where()
	rows, err := s.db.Query(
		`SELECT event_id, ts, device, source, model, provider, account_label,
		        input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		        cache_1h_tokens, cache_5m_tokens, duration_ms, ttft_ms,
		        session_id, cwd, git_branch, repo, app_version
		 FROM events WHERE `+where+`
		 ORDER BY ts DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.EventID, &e.TS, &e.Device, &e.Source, &e.Model, &e.Provider, &e.AccountLabel,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheCreationTokens,
			&e.Cache1hTokens, &e.Cache5mTokens, &e.DurationMS, &e.TTFTMS,
			&e.SessionID, &e.CWD, &e.GitBranch, &e.Repo, &e.AppVersion); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventCount returns how many events match the filter (for pagination).
func (s *Store) EventCount(f EventFilter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE `+where, args...).Scan(&n)
	return n, err
}
