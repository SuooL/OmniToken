// Package codex parses Codex CLI rollout JSONL files
// (~/.codex/sessions/**/rollout-*.jsonl) into unified usage events.
//
// File structure (streamed, order matters):
//   - session_meta: session_id, rollout id, cwd, cli_version, model_provider
//   - turn_context: current model (and cwd, which can change per turn)
//   - event_msg{token_count}: info.last_token_usage holds the per-request
//     delta; info.total_token_usage the running total.
//   - event_msg{task_started} … event_msg{task_complete|turn_aborted}: one turn,
//     bracketed by file position — the token_count lines in between belong to
//     it and carry no turn_id of their own. The closing record holds Codex's
//     own timing (started_at, completed_at, duration_ms,
//     time_to_first_token_ms), which is what gen_ms and ttft_ms are built from
//     (ADR-0009, 2026-07-31 revision).
//
// Semantics validated against ccusage's Rust adapter
// (rust/crates/ccusage/src/adapter/codex/parser.rs):
//   - token_count events repeat without the cumulative total advancing
//     (e.g. rate-limit refreshes) — those must be skipped, not re-counted;
//   - when last_token_usage is missing, the delta of consecutive
//     total_token_usage values recovers it;
//   - OpenAI-style input_tokens INCLUDES cached_input_tokens (clamped), so
//     non-cached input = input - cached; reasoning tokens are part of output.
//
// Forking a thread (by hand, or by spawning a subagent) copies the parent's
// whole history into the new rollout, token_count lines included, changing only
// the line timestamps. ADR-0004 assumed the opposite; ADR-0020 measured it and
// added dedupKey, which is what makes those copies count once.
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
		// task_started / task_complete / turn_aborted: Codex's own timing of a
		// turn (ADR-0009, 2026-07-31 revision). Seconds for the two clocks,
		// milliseconds for the two spans, all written by Codex rather than
		// derived from this line's timestamp.
		TurnID      string `json:"turn_id"`
		StartedAt   int64  `json:"started_at"`
		CompletedAt int64  `json:"completed_at"`
		DurationMS  int64  `json:"duration_ms"`
		TTFTMS      int64  `json:"time_to_first_token_ms"`
		// Authoritative quota state reported by the server (ADR-0007).
		RateLimits *rateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// openTurn collects the usage events written between task_started and its
// closing record. token_count lines carry no turn_id, so membership is by file
// position — which survives replay, because a replayed block keeps its original
// order even though every line in it gets the flush timestamp.
type openTurn struct {
	// id is task_started's turn_id, carried down to the token_count lines so
	// they can be given a dedup key (ADR-0020).
	id  string
	idx []int   // positions in ParseResult.Events
	ts  []int64 // each event's own line timestamp, ms
}

type rateLimits struct {
	LimitID   string      `json:"limit_id"`
	Primary   *limitState `json:"primary"`
	Secondary *limitState `json:"secondary"`
	PlanType  string      `json:"plan_type"`
	// Credits is only ever populated by the real OpenAI account. Its contents
	// do not matter here — presence is the signal (see planEvidence).
	Credits *json.RawMessage `json:"credits"`
}

type limitState struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix seconds
}

// Parse streams JSONL lines from r. Same offset contract as the claudecode
// parser: a final partial line is not counted into consumed.
// The turn-start carry (ADR-0009) is unused here: Codex files are re-parsed
// whole on every growth (FullReparse), so a turn still open at EOF gets its
// generation interval on the next pass, once its closing record exists.
func Parse(r io.Reader, device string, _ int64) (res model.ParseResult) {
	br := bufio.NewReaderSize(r, 1<<20)
	var ctx struct {
		rolloutID, sessionID, cwd, version, provider, model string
	}
	var prevTotal *usage
	var prevMS int64
	var turn *openTurn
	// Whether the real OpenAI account ever answered in this file (see
	// planEvidence). It usually shows up after the first usage event, so the
	// verdict is applied to the whole file on the way out.
	var subscriptionSeen bool
	seq := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			applySessionChannel(res.Events, ctx.provider, subscriptionSeen)
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
		lineMS := int64(0)
		if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			lineMS = t.UnixMilli()
			prevMS = lineMS
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
			switch e.Payload.Type {
			case "task_started":
				// An unclosed predecessor is abandoned rather than merged: its
				// events keep no interval instead of borrowing the next turn's.
				turn = &openTurn{id: e.Payload.TurnID}
				continue
			case "task_complete", "turn_aborted":
				closeTurn(res.Events, turn, lineMS,
					e.Payload.CompletedAt, e.Payload.DurationMS, e.Payload.TTFTMS)
				turn = nil
				continue
			case "token_count":
			default:
				continue
			}
			if q := e.Payload.RateLimits; q != nil {
				if planEvidence(q) {
					subscriptionSeen = true
				}
				if prevMS > 0 {
					res.Quotas = append(res.Quotas, quotaSnapshots(q, device, prevMS)...)
				}
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
			// gen_ms is not known yet: it is the turn's, and the turn is still
			// open. closeTurn writes it back over these events when the
			// closing record arrives (ADR-0009, 2026-07-31 revision).
			turnID := ""
			if turn != nil {
				turn.idx = append(turn.idx, len(res.Events))
				turn.ts = append(turn.ts, ts.UnixMilli())
				turnID = turn.id
			}
			res.Events = append(res.Events, model.Event{
				EventID:             eventID(ctx.rolloutID, e.Timestamp, seq, u),
				DedupKey:            dedupKey(turnID, total),
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

// replayToleranceMS is how far a closing record's own line timestamp may sit
// from the completion instant recorded inside it before the turn is treated as
// replayed rather than lived.
//
// Resuming a thread rewrites the whole prior history into a new rollout with
// the flush time on every line, while started_at / completed_at keep the real
// epoch. Measured over 200 rollouts the two clocks agree within 963ms at P90
// (completed_at is truncated to whole seconds, so a sub-second lag is normal)
// and the 6.6% of turns that disagree do so by more than an hour — up to 11
// days. Nothing lands in between, so the threshold is not a tuned parameter.
//
// Those turns are left unmeasured on purpose. Their tokens are unaffected, but
// their line timestamps are fiction, and the speed views position an interval
// by the event's ts: measuring them would paint an 11-day-old turn onto the
// last hour of the live curve. ADR-0009's rule holds — no data is not zero
// speed, and it is reported as coverage instead.
const replayToleranceMS = 60 * 1000

// closeTurn writes the turn's generation interval back over the usage events
// that fell inside it (ADR-0009, 2026-07-31 revision).
//
// The interval is Codex's own duration_ms - time_to_first_token_ms, and it is
// spread over the turn's events so that two things hold at once for every view
// that computes Σoutput ÷ |union of intervals|:
//
//   - the union of [ts-gen_ms, ts] over the turn is exactly the authoritative
//     interval — each event's slice ends at its own line timestamp and starts
//     at the previous event's, with the remainder (generation before the first
//     token_count was written) going to the first event;
//   - every event of the turn keeps a non-zero interval, because those views
//     filter on gen_ms > 0 and would otherwise drop the response's tokens out
//     of the numerator while keeping the time in the denominator.
//
// The result is a lower bound: tool calls run inside the turn and Codex counts
// them in duration_ms. Deducting them (patch_apply_end, mcp_tool_call_end both
// carry durations) is the refinement ADR-0009 explicitly left for later.
// maxSpanMS deliberately does NOT apply here: it exists to bound an interval
// *guessed* from log gaps, while this one is measured by Codex, and clamping it
// would drop real elapsed time while keeping every token — inflating speed.
func closeTurn(events []model.Event, t *openTurn, closeMS, completedAtSec, durationMS, ttftMS int64) {
	if t == nil || len(t.idx) == 0 {
		return
	}
	// No TTFT means the wait for the first token cannot be separated from the
	// generation: turn_aborted never carries it, and neither do the older
	// task_complete records. Those turns stay uncovered rather than reporting
	// the latency as if it were generation.
	if durationMS <= 0 || ttftMS <= 0 || ttftMS >= durationMS || completedAtSec <= 0 {
		return
	}
	completedMS := completedAtSec * 1000
	if closeMS-completedMS > replayToleranceMS || completedMS-closeMS > replayToleranceMS {
		return
	}
	// A partially replayed turn: the closer looks live but some of its events
	// carry timestamps from outside the turn's own window.
	lo, hi := closeMS-durationMS-replayToleranceMS, closeMS+replayToleranceMS
	for _, ms := range t.ts {
		if ms < lo || ms > hi {
			return
		}
	}

	genMS := durationMS - ttftMS
	n := len(t.idx)
	// TTFT is exact and belongs to the turn, so it is recorded once, on the
	// response whose latency it measured — repeating it on every event would
	// weight a turn's latency by how many requests it happened to make.
	events[t.idx[0]].TTFTMS = ttftMS
	if n == 1 {
		events[t.idx[0]].GenMS = genMS
		return
	}

	shares := make([]int64, n)
	spread := t.ts[n-1] - t.ts[0]
	if spread <= genMS {
		for i := 1; i < n; i++ {
			shares[i] = t.ts[i] - t.ts[i-1]
		}
		shares[0] = genMS - max(spread, 0)
	} else {
		// The turn's log lines span more than its generation interval (a tool
		// still running after the last response). Scaling keeps the slices
		// disjoint, so the union is still exactly the authoritative interval.
		var assigned int64
		for i := 1; i < n; i++ {
			shares[i] = max(t.ts[i]-t.ts[i-1], 0) * genMS / spread
			assigned += shares[i]
		}
		shares[0] = genMS - assigned
	}
	// Two responses can land in the same millisecond, which would leave the
	// second with a zero-length slice. It gets one millisecond that overlaps
	// its neighbour rather than one taken from it: the union is what the views
	// divide by, and it must stay equal to the authoritative interval.
	for i := range shares {
		shares[i] = max(shares[i], 1)
	}
	for i, at := range t.idx {
		events[at].GenMS = shares[i]
	}
}

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
// file's UUID + timestamp + in-file sequence + usage payload. It identifies a
// LOG LINE, and that is all it can identify: a forked thread copies the parent's
// lines into a new rollout with new timestamps, so the two copies of one
// generation necessarily land on different ids. dedupKey below is what catches
// that; this derivation must never change, because every row already in the
// database was written under it (ADR-0004).
func eventID(rolloutID, ts string, seq int, u *usage) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d", rolloutID, ts, seq, u.InputTokens, u.CachedInput, u.OutputTokens, u.TotalTokens)))
	return "cx:" + hex.EncodeToString(h[:12])
}

// dedupKey identifies the GENERATION, so that the same one recorded in two
// rollout files is counted once (ADR-0020).
//
// Resuming or forking a thread — and spawning a subagent, which is the same
// mechanism — copies the parent's entire history into the new rollout verbatim.
// Measured over 610 local rollouts: 845 of 33,445 usage events are such copies
// (3.12% of output tokens), the copies never disagree with the original on a
// single usage field, and the only thing rewritten is the line timestamp, which
// becomes the fork's flush instant.
//
// So the key is built from the two things the copy preserves and eventID cannot
// use: the turn's id, and the thread's running total at that point.
//
//   - the running total is what separates generations inside one thread. It only
//     ever advances (the parser already skips token_count lines whose total
//     stands still), so no two generations of a thread share one. Measured: zero
//     same-file collisions, and all 647 collisions were cross-file copies.
//   - turn_id is what separates threads. It is required to be a UUID, and that
//     is the whole point: Codex's MCP sessions number turns with a per-session
//     counter instead, so "2" belongs to 13 unrelated files on this machine.
//     Keying on that would merge unrelated generations — an undercount, which is
//     worse than the overcount being fixed because nothing reveals it. The 341
//     events without a UUID turn id keep event_id-only dedup; not one of them is
//     in a forked file, so nothing is lost.
//
// An empty key means "no second opinion", never "duplicate": if Codex changes
// shape the mechanism degrades to today's behaviour rather than merging rows.
func dedupKey(turnID string, total *usage) string {
	if total == nil || !isUUID(turnID) {
		return ""
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%d|%d", turnID,
		total.InputTokens, total.CachedInput, total.CacheWriteInput,
		total.OutputTokens, total.ReasoningTokens, total.TotalTokens)))
	return "cxg:" + hex.EncodeToString(h[:12])
}

// isUUID reports whether s is a canonical lower-case UUID, the form Codex uses
// for turn and rollout ids.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if s[i] != '-' {
				return false
			}
		case s[i] >= '0' && s[i] <= '9':
		case s[i] >= 'a' && s[i] <= 'f':
		default:
			return false
		}
	}
	return true
}

// builtinOpenAIProvider is Codex's own provider id. It is matched exactly,
// case included, and that is load-bearing rather than pedantic: the machine
// this was measured on has a `[model_providers.OpenAI]` block in config.toml
// pointing at a relay, and 137 of its sessions (9,871 usage records) are
// branded "OpenAI" while never reaching OpenAI. Folding case merges those into
// the subscription column, which is exactly the error being fixed.
const builtinOpenAIProvider = "openai"

// providerLabel keeps the rollout's declared model_provider verbatim. It is a
// name the user chose, not a probe result, so the only thing it establishes is
// which endpoint the user pointed Codex at — and any name other than Codex's
// built-in one says "somewhere else" (model.BillingChannel maps it to relay).
func providerLabel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return model.ProviderUnknown
	}
	return p
}

// planEvidence reports whether a rate_limits payload came from a real ChatGPT
// subscription.
//
// ADR-0018 proposed testing for the payload's mere presence and required that
// to be validated before use. It was, and it failed: on 610 local rollouts,
// every one of the 523 sessions with any token_count line carries rate_limits —
// relays synthesise the envelope too, so the test scores 20.8% accuracy, worse
// than a constant "no". What relays do not synthesise is the account state
// inside it. Across 22,547 records from custom/sub2api/enjoy/aihub/trellisreview
// both plan_type and credits are null without exception, while plan_type alone
// covers 98.2% of genuine subscription sessions and credits covers the
// remaining pre-0.140 CLI versions — together, 100% recall at 100% precision.
func planEvidence(q *rateLimits) bool {
	return q.PlanType != "" || q.Credits != nil
}

// applySessionChannel resolves the session's billing channel once the whole
// file has been read, because the plan evidence usually arrives after the first
// usage event. Rollouts are re-parsed whole on every growth
// (collect.SourceSpec.FullReparse), so a file-scoped conclusion sees every line.
//
// Both signals must agree before anything is called a subscription: Codex's
// built-in provider id AND account state that only the real account emits. Each
// alone has a known counterexample on this machine — a relay named "OpenAI",
// and a relay that forwarded a shared account's plan_type — and requiring both
// removes every observed false positive. When they disagree the label stays as
// declared, which BillingChannel reads as "not first-party".
func applySessionChannel(events []model.Event, provider string, subscription bool) {
	if !subscription || strings.TrimSpace(provider) != builtinOpenAIProvider {
		return
	}
	for i := range events {
		events[i].Provider = model.ProviderOpenAIChatGPT
	}
}
