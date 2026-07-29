package store

import "time"

// SessionSpeed is one generation stream's throughput over the live window.
//
// A "stream" is a session_id. Claude Code files subagent output under the
// parent's session_id, so a busy session can hold several concurrent streams —
// which is exactly why the duration is a union and not a sum.
type SessionSpeed struct {
	SessionID    string  `json:"session_id"`
	Device       string  `json:"device"`
	Repo         string  `json:"repo,omitempty"`
	Model        string  `json:"model,omitempty"`
	Source       string  `json:"source"`
	OutputTokens int64   `json:"output_tokens"`
	ActiveMS     int64   `json:"active_ms"`
	TPS          float64 `json:"tps"`
	LastTS       int64   `json:"last_ts"`
}

// LiveSpeed answers "how fast is this machine generating right now".
//
// TPS is output tokens over the *union* of generation intervals, not over the
// window: a machine that generated hard for 20s of a 60s window is running at
// its generation speed, not at a third of it. That makes this a different
// question from the burn rate, which deliberately divides by the whole window
// and therefore counts idle time.
//
// Sessions overlap in wall-clock time, so summing their durations would count
// the same seconds twice — three sessions at 50 tok/s each is 150 tok/s of
// machine throughput, while each stream is still doing 50. Both numbers are
// reported because they answer different questions (ADR-0009).
type LiveSpeed struct {
	WindowSeconds int            `json:"window_seconds"`
	OutputTokens  int64          `json:"output_tokens"`
	ActiveMS      int64          `json:"active_ms"`
	TPS           float64        `json:"tps"`
	Sessions      []SessionSpeed `json:"sessions"`
}

// LiveSpeedSince computes machine-wide and per-session generation speed over
// [since, now]. device empty means every device.
//
// Intervals are clipped to the window so a generation that started before it
// only contributes the part that falls inside — otherwise a long response
// straddling the boundary would lend the window more active time than elapsed.
func (s *Store) LiveSpeedSince(since, now time.Time, device string) (LiveSpeed, error) {
	out := LiveSpeed{
		WindowSeconds: int(now.Sub(since).Seconds()),
		Sessions:      []SessionSpeed{},
	}
	startMS, endMS := since.UnixMilli(), now.UnixMilli()

	q := `SELECT session_id, device, repo, model, source, output_tokens, gen_ms, ts
	      FROM events
	      WHERE ts >= ? AND ts <= ? AND gen_ms > 0 AND output_tokens > 0`
	args := []any{startMS, endMS}
	if device != "" {
		q += ` AND device = ?`
		args = append(args, device)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	type acc struct {
		SessionSpeed
		spans []span
	}
	bySession := map[string]*acc{}
	var all []span

	for rows.Next() {
		var (
			sess, dev, repo, mdl, src string
			outTok, genMS, ts         int64
		)
		if err := rows.Scan(&sess, &dev, &repo, &mdl, &src, &outTok, &genMS, &ts); err != nil {
			return out, err
		}
		start, end := ts-genMS, ts
		if start < startMS {
			start = startMS
		}
		if end > endMS {
			end = endMS
		}
		if end <= start {
			continue
		}

		key := dev + "\x00" + sess
		a := bySession[key]
		if a == nil {
			a = &acc{SessionSpeed: SessionSpeed{SessionID: sess, Device: dev, Repo: repo, Source: src}}
			bySession[key] = a
		}
		a.OutputTokens += outTok
		a.spans = append(a.spans, span{start, end})
		// Last model wins: a session can switch models, and what it is running
		// now is more useful on a live view than what it started with.
		if ts >= a.LastTS {
			a.LastTS, a.Model = ts, mdl
		}

		out.OutputTokens += outTok
		all = append(all, span{start, end})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	for _, a := range bySession {
		a.ActiveMS = unionMS(a.spans)
		if a.ActiveMS > 0 {
			a.TPS = float64(a.OutputTokens) * 1000 / float64(a.ActiveMS)
		}
		out.Sessions = append(out.Sessions, a.SessionSpeed)
	}
	sortSessionsByTokens(out.Sessions)

	out.ActiveMS = unionMS(all)
	if out.ActiveMS > 0 {
		out.TPS = float64(out.OutputTokens) * 1000 / float64(out.ActiveMS)
	}
	return out, nil
}

func sortSessionsByTokens(ss []SessionSpeed) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j].OutputTokens > ss[j-1].OutputTokens; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}
