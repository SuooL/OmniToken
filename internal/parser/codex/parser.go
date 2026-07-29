// Package codex parses Codex CLI rollout JSONL files
// (~/.codex/sessions/**/rollout-*.jsonl) into unified usage events.
//
// File structure (streamed, order matters):
//   - session_meta: session_id, rollout id, cwd, cli_version, model_provider
//   - turn_context: current model (and cwd, which can change per turn)
//   - event_msg{token_count}: info.last_token_usage holds the per-request
//     delta; info.total_token_usage the running total.
//
// Semantics validated against ccusage's Rust adapter
// (rust/crates/ccusage/src/adapter/codex/parser.rs):
//   - token_count events repeat without the cumulative total advancing
//     (e.g. rate-limit refreshes) — those must be skipped, not re-counted;
//   - when last_token_usage is missing, the delta of consecutive
//     total_token_usage values recovers it;
//   - OpenAI-style input_tokens INCLUDES cached_input_tokens (clamped), so
//     non-cached input = input - cached; reasoning tokens are part of output.
package codex

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

const Source = "codex"

type usage struct {
	InputTokens     int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

type entry struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		// session_meta
		ID            string `json:"id"`
		SessionID     string `json:"session_id"`
		CWD           string `json:"cwd"`
		CLIVersion    string `json:"cli_version"`
		ModelProvider string `json:"model_provider"`
		// turn_context
		Model string `json:"model"`
		// event_msg
		Type string `json:"type"`
		Info *struct {
			Model string `json:"model"` // present in older formats
			Last  *usage `json:"last_token_usage"`
			Total *usage `json:"total_token_usage"`
		} `json:"info"`
		// Authoritative quota state reported by the server (ADR-0007).
		RateLimits *rateLimits `json:"rate_limits"`
	} `json:"payload"`
}

type rateLimits struct {
	LimitID   string      `json:"limit_id"`
	Primary   *limitState `json:"primary"`
	Secondary *limitState `json:"secondary"`
	PlanType  string      `json:"plan_type"`
}

type limitState struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix seconds
}

// Parse streams JSONL lines from r. Same offset contract as the claudecode
// parser: a final partial line is not counted into consumed.
// The turn-start carry (ADR-0009) is unused here: Codex files are re-parsed
// whole on every growth (FullReparse), and Codex emits no gen_ms anyway.
func Parse(r io.Reader, device string, _ int64) (res model.ParseResult) {
	br := bufio.NewReaderSize(r, 1<<20)
	var ctx struct {
		rolloutID, sessionID, cwd, version, provider, model string
	}
	var prevTotal *usage
	var prevMS int64
	seq := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return res
		}
		res.Consumed += int64(len(line))
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		// Advance the session clock on EVERY timestamped line (turn context,
		// response items, …) so durations span real activity; remember the
		// previous tick for the current line's own duration.
		prevTick := prevMS
		if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			prevMS = t.UnixMilli()
		}
		switch e.Type {
		case "session_meta":
			ctx.rolloutID = e.Payload.ID
			ctx.sessionID = e.Payload.SessionID
			ctx.cwd = e.Payload.CWD
			ctx.version = e.Payload.CLIVersion
			ctx.provider = e.Payload.ModelProvider
			prevTotal = nil
		case "turn_context":
			if e.Payload.Model != "" {
				ctx.model = e.Payload.Model
			}
			if e.Payload.CWD != "" {
				ctx.cwd = e.Payload.CWD
			}
		case "event_msg":
			if e.Payload.Type != "token_count" {
				continue
			}
			if q := e.Payload.RateLimits; q != nil && prevMS > 0 {
				res.Quotas = append(res.Quotas, quotaSnapshots(q, device, prevMS)...)
			}
			info := e.Payload.Info
			if info == nil {
				continue
			}
			u, total := deltaUsage(info.Last, info.Total, prevTotal)
			if total != nil {
				prevTotal = total
			}
			if u == nil || (u.InputTokens == 0 && u.CachedInput == 0 && u.OutputTokens == 0 && u.ReasoningTokens == 0) {
				continue
			}
			ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
			if err != nil {
				continue
			}
			m := info.Model
			if m == "" {
				m = ctx.model
			}
			cached := min(u.CachedInput, u.InputTokens)
			seq++
			durationMS := int64(0)
			if prevTick > 0 && ts.UnixMilli() > prevTick {
				durationMS = min(ts.UnixMilli()-prevTick, maxSpanMS)
			}
			// No gen_ms for Codex (ADR-0009). Rollout files replay history on
			// session resume, writing whole blocks with the flush time rather
			// than the time things happened: 70% of token_count records across
			// the last 60 rollouts share a timestamp with the line above them.
			// Any interval measured against those is fiction, and a wrong speed
			// is worse than an absent one.
			res.Events = append(res.Events, model.Event{
				EventID:             eventID(ctx.rolloutID, e.Timestamp, seq, u),
				DurationMS:          durationMS,
				TS:                  ts.UnixMilli(),
				Device:              device,
				Source:              Source,
				Model:               m,
				Provider:            providerLabel(ctx.provider),
				InputTokens:         u.InputTokens - cached,
				OutputTokens:        u.OutputTokens,
				CacheReadTokens:     cached,
				CacheCreationTokens: u.CacheWriteInput,
				SessionID:           ctx.sessionID,
				CWD:                 ctx.cwd,
				AppVersion:          ctx.version,
			})
		}
	}
}

// maxSpanMS clamps one event's derived duration (ADR-0006).
const maxSpanMS = 30 * 60 * 1000

// quotaSnapshots turns one rate_limits payload into snapshots (ADR-0007).
// Observed windows in the wild: 300 minutes (5h) and 10080 (weekly).
func quotaSnapshots(q *rateLimits, device string, observedMS int64) []model.QuotaSnapshot {
	var out []model.QuotaSnapshot
	add := func(scope string, st *limitState) {
		if st == nil || st.WindowMinutes <= 0 {
			return
		}
		out = append(out, model.QuotaSnapshot{
			Device:        device,
			Source:        Source,
			LimitID:       q.LimitID,
			Scope:         scope,
			WindowMinutes: st.WindowMinutes,
			UsedPercent:   st.UsedPercent,
			ResetsAt:      st.ResetsAt * 1000,
			ObservedAt:    observedMS,
			PlanType:      q.PlanType,
		})
	}
	add("primary", q.Primary)
	add("secondary", q.Secondary)
	return out
}

// deltaUsage picks the per-request usage following ccusage's rules: prefer
// last_token_usage but only when the cumulative total advanced (repeated
// token_count events carry a stale copy); with no last_token_usage, fall
// back to the totals delta.
func deltaUsage(last, total, prevTotal *usage) (*usage, *usage) {
	advanced := total == nil || prevTotal == nil || *total != *prevTotal
	if last != nil && advanced {
		return last, total
	}
	if total != nil && advanced && prevTotal != nil {
		d := usage{
			InputTokens:     max(total.InputTokens-prevTotal.InputTokens, 0),
			CachedInput:     max(total.CachedInput-prevTotal.CachedInput, 0),
			CacheWriteInput: max(total.CacheWriteInput-prevTotal.CacheWriteInput, 0),
			OutputTokens:    max(total.OutputTokens-prevTotal.OutputTokens, 0),
			ReasoningTokens: max(total.ReasoningTokens-prevTotal.ReasoningTokens, 0),
			TotalTokens:     max(total.TotalTokens-prevTotal.TotalTokens, 0),
		}
		return &d, total
	}
	if total != nil && advanced {
		return total, total // first event of the file: total IS the delta
	}
	return nil, total
}

// eventID: token_count lines carry no message id, so identity is the rollout
// file's UUID + timestamp + in-file sequence + usage payload. Resumed threads
// get a new rollout id and Codex does not copy old token_count lines into new
// rollouts, so cross-file duplication is not a concern; the hash guards
// against re-reads of the same file after truncation.
func eventID(rolloutID, ts string, seq int, u *usage) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d", rolloutID, ts, seq, u.InputTokens, u.CachedInput, u.OutputTokens, u.TotalTokens)))
	return "cx:" + hex.EncodeToString(h[:12])
}

func providerLabel(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "unknown"
	}
	return p
}
