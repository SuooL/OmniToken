package store

import (
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// SpeedContribution is one additive breakdown row. ContributionTPS uses the
// parent LiveSpeed window's global active-time denominator; NativeTPS uses only
// this group's merged generation intervals.
type SpeedContribution struct {
	Key             string  `json:"key"`
	OutputTokens    int64   `json:"output_tokens"`
	ActiveMS        int64   `json:"active_ms"`
	ContributionTPS float64 `json:"contribution_tps"`
	NativeTPS       float64 `json:"native_tps"`
}

type speedContributionAcc struct {
	outputTokens int64
	spans        []span
}

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
	// ContributionTPS uses the machine-wide active-time denominator, so all
	// session contributions reconcile exactly to LiveSpeed.TPS. TPS above
	// remains the session's native speed for wire compatibility.
	ContributionTPS float64 `json:"contribution_tps"`
	LastTS          int64   `json:"last_ts"`
	// Spans are this stream's merged generation intervals, [startMS, endMS]
	// pairs, so the live view can draw when it was actually producing rather
	// than only how much it produced.
	Spans [][2]int64 `json:"spans"`
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
	WindowSeconds int                 `json:"window_seconds"`
	WindowStartMS int64               `json:"window_start_ms"`
	WindowEndMS   int64               `json:"window_end_ms"`
	OutputTokens  int64               `json:"output_tokens"`
	ActiveMS      int64               `json:"active_ms"`
	TPS           float64             `json:"tps"`
	Sessions      []SessionSpeed      `json:"sessions"`
	Sources       []SpeedContribution `json:"sources"`
	Devices       []SpeedContribution `json:"devices"`
	Models        []SpeedContribution `json:"models"`
	// Spans is the union across every stream — the shared wall-clock track that
	// distinguishes three concurrent 50 tok/s sessions (150 aggregate) from
	// three sequential 50 tok/s sessions (50 aggregate).
	Spans [][2]int64 `json:"spans"`
}

// LiveSpeedSince computes machine-wide and per-session generation speed over
// [since, now]. device empty means every device.
//
// Intervals are clipped to the window so a generation that started before it
// only contributes the part that falls inside — otherwise a long response
// straddling the boundary would lend the window more active time than elapsed.
func (s *Store) LiveSpeedSince(since, now time.Time, device string) (LiveSpeed, error) {
	startMS, endMS := since.UnixMilli(), now.UnixMilli()
	out := LiveSpeed{
		WindowSeconds: int(now.Sub(since).Seconds()),
		WindowStartMS: startMS,
		WindowEndMS:   endMS,
		Sessions:      []SessionSpeed{},
		Sources:       []SpeedContribution{},
		Devices:       []SpeedContribution{},
		Models:        []SpeedContribution{},
		Spans:         [][2]int64{},
	}

	// Every source with an interval counts, Codex included since ADR-0009's
	// 2026-07-31 revision: its interval comes from the turn's own task_complete
	// timing, not from the log line timestamps that made it unmeasurable before.
	// The filter that matters is gen_ms > 0 — an unmeasured row is absent from
	// the numerator and the denominator alike, never a zero in either.
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
	bySource := map[string]*speedContributionAcc{}
	byDevice := map[string]*speedContributionAcc{}
	byModel := map[string]*speedContributionAcc{}
	var all []span

	for rows.Next() {
		var (
			sess, dev, repo, mdl, src string
			outTok, genMS, ts         int64
		)
		if err := rows.Scan(&sess, &dev, &repo, &mdl, &src, &outTok, &genMS, &ts); err != nil {
			return out, err
		}
		eventStart, eventEnd := ts-genMS, ts
		start, end := eventStart, eventEnd
		if start < startMS {
			start = startMS
		}
		if end > endMS {
			end = endMS
		}
		if end <= start {
			continue
		}
		allocatedTokens := outTok * (end - start) / (eventEnd - eventStart)

		key := dev + "\x00" + sess
		a := bySession[key]
		if a == nil {
			a = &acc{SessionSpeed: SessionSpeed{SessionID: sess, Device: dev, Repo: repo, Source: src}}
			bySession[key] = a
		}
		a.OutputTokens += allocatedTokens
		a.spans = append(a.spans, span{start, end})
		// Last model wins: a session can switch models, and what it is running
		// now is more useful on a live view than what it started with.
		if ts >= a.LastTS {
			a.LastTS, a.Model = ts, mdl
		}

		addSpeedContribution(bySource, speedSourceKey(src), allocatedTokens, span{start, end})
		addSpeedContribution(byDevice, dev, allocatedTokens, span{start, end})
		addSpeedContribution(byModel, model.CanonicalModel(mdl), allocatedTokens, span{start, end})

		out.OutputTokens += allocatedTokens
		all = append(all, span{start, end})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	machine := mergeSpans(all)
	out.Spans = exportSpans(machine)
	for _, sp := range machine {
		out.ActiveMS += sp.end - sp.start
	}
	if out.ActiveMS > 0 {
		out.TPS = float64(out.OutputTokens) * 1000 / float64(out.ActiveMS)
	}

	for _, a := range bySession {
		merged := mergeSpans(a.spans)
		a.Spans = exportSpans(merged)
		for _, sp := range merged {
			a.ActiveMS += sp.end - sp.start
		}
		if a.ActiveMS > 0 {
			a.TPS = float64(a.OutputTokens) * 1000 / float64(a.ActiveMS)
		}
		if out.ActiveMS > 0 {
			a.ContributionTPS = float64(a.OutputTokens) * 1000 / float64(out.ActiveMS)
		}
		out.Sessions = append(out.Sessions, a.SessionSpeed)
	}
	sortSessionsByTokens(out.Sessions)
	out.Sources = buildSpeedContributions(bySource, out.ActiveMS)
	out.Devices = buildSpeedContributions(byDevice, out.ActiveMS)
	out.Models = buildSpeedContributions(byModel, out.ActiveMS)
	return out, nil
}

func speedSourceKey(source string) string {
	switch source {
	case "claude-code", "codex":
		return source
	default:
		return "api"
	}
}

func addSpeedContribution(groups map[string]*speedContributionAcc, key string, outputTokens int64, interval span) {
	group := groups[key]
	if group == nil {
		group = &speedContributionAcc{}
		groups[key] = group
	}
	group.outputTokens += outputTokens
	group.spans = append(group.spans, interval)
}

func buildSpeedContributions(groups map[string]*speedContributionAcc, globalActiveMS int64) []SpeedContribution {
	out := make([]SpeedContribution, 0, len(groups))
	for key, group := range groups {
		merged := mergeSpans(group.spans)
		row := SpeedContribution{Key: key, OutputTokens: group.outputTokens}
		for _, sp := range merged {
			row.ActiveMS += sp.end - sp.start
		}
		if row.ActiveMS > 0 {
			row.NativeTPS = float64(row.OutputTokens) * 1000 / float64(row.ActiveMS)
		}
		if globalActiveMS > 0 {
			row.ContributionTPS = float64(row.OutputTokens) * 1000 / float64(globalActiveMS)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ContributionTPS == out[j].ContributionTPS {
			return out[i].Key < out[j].Key
		}
		return out[i].ContributionTPS > out[j].ContributionTPS
	})
	return out
}

func exportSpans(spans []span) [][2]int64 {
	out := make([][2]int64, 0, len(spans))
	for _, sp := range spans {
		out = append(out, [2]int64{sp.start, sp.end})
	}
	return out
}

func sortSessionsByTokens(ss []SessionSpeed) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j].OutputTokens > ss[j-1].OutputTokens; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}
