// Package claudecode parses Claude Code session JSONL files
// (~/.claude/projects/**/*.jsonl) into unified usage events.
package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

const Source = "claude-code"

// entry mirrors only the fields we need from one JSONL line.
type entry struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	RequestID string `json:"requestId"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	SessionID string `json:"sessionId"`
	Version   string `json:"version"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation       struct {
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// maxSpanMS clamps one event's derived duration (ADR-0006): guards against
// clock anomalies and cross-scan gaps being read as one huge generation.
const maxSpanMS = 30 * 60 * 1000

// Parse reads JSONL lines from r and returns usage events attributed to device.
// Malformed lines are skipped; a final partial line (no trailing newline yet,
// still being written) is not consumed — its byte count is excluded from the
// returned offset delta so it will be re-read on the next scan.
//
// Every line's timestamp (user turns, tool results, assistant steps) advances
// a session clock; each usage event carries duration_ms since the previous
// entry, turning it into a work interval [ts-duration, ts] (ADR-0006).
func Parse(r io.Reader, device string, turnStartMS int64) (res model.ParseResult) {
	br := bufio.NewReaderSize(r, 1<<20)
	var prevMS int64
	// turnStartMS is the start of the turn in flight, for gen_ms (ADR-0009).
	// Tool results are "user" entries too, so this tracks each round trip
	// rather than only what a human typed. It arrives from the previous scan of
	// this file and is handed back for the next one.
	//
	// Not reset when an event is emitted: several assistant records can belong
	// to one turn, and giving them the same start is what lets the union
	// collapse them instead of counting each one's tokens against a few
	// milliseconds.
	res.TurnStartMS = turnStartMS
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			// Partial trailing line: leave it for the next scan.
			res.TurnStartMS = turnStartMS
			return res
		}
		res.Consumed += int64(len(line))
		lineMS := extractTimestampMS(line)
		if isUserLine(line) && lineMS > 0 {
			turnStartMS = lineMS
		}
		if ev, ok := parseLine(line, device); ok {
			if prevMS > 0 && ev.TS > prevMS {
				ev.DurationMS = min(ev.TS-prevMS, maxSpanMS)
			}
			if turnStartMS > 0 && ev.TS > turnStartMS {
				ev.GenMS = min(ev.TS-turnStartMS, maxSpanMS)
			}
			res.Events = append(res.Events, ev)
		}
		if q, ok := parseLimitHit(line, device, lineMS); ok {
			res.Quotas = append(res.Quotas, q)
		}
		if lineMS > 0 {
			prevMS = lineMS
		}
	}
}

// limitMarker is the only place Claude Code reveals an authoritative quota
// reset time: an API error line reading
// "Claude AI usage limit reached|<unix seconds>" (ADR-0007; semantics
// verified against ccusage adapter/claude/mod.rs).
const limitMarker = "Claude AI usage limit reached|"

func parseLimitHit(line, device string, lineMS int64) (model.QuotaSnapshot, bool) {
	i := strings.Index(line, limitMarker)
	if i < 0 || !strings.Contains(line, `"isApiErrorMessage":true`) {
		return model.QuotaSnapshot{}, false
	}
	rest := line[i+len(limitMarker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return model.QuotaSnapshot{}, false
	}
	secs, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil || secs <= 0 {
		return model.QuotaSnapshot{}, false
	}
	if lineMS == 0 {
		return model.QuotaSnapshot{}, false
	}
	return model.QuotaSnapshot{
		Device:      device,
		Source:      Source,
		LimitID:     "claude-usage-limit",
		Scope:       "limit-hit",
		UsedPercent: 100,
		ResetsAt:    secs * 1000,
		ObservedAt:  lineMS,
	}, true
}

// isUserLine reports whether the line is a user turn — a typed message or a
// tool result, both of which mark the start of a round trip to the API and so
// begin a gen_ms interval (ADR-0009).
//
// Substring test rather than a parse, for the same reason as
// extractTimestampMS: these are the megabyte lines. It could in principle
// misfire on a tool result quoting this exact key, so it was measured before
// being relied on — 9,676 matching lines across 400 real session files, zero
// of them anything but a top-level user entry. A rare false positive would
// only shorten one interval, which the union then largely absorbs.
func isUserLine(line string) bool {
	return strings.Contains(line, `"type":"user"`)
}

// extractTimestampMS pulls the top-level "timestamp" field without a full
// JSON parse — tool-result lines can be megabytes and only the clock matters.
func extractTimestampMS(line string) int64 {
	i := strings.LastIndex(line, `"timestamp":"`)
	if i < 0 {
		return 0
	}
	rest := line[i+len(`"timestamp":"`):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return 0
	}
	ts, err := time.Parse(time.RFC3339Nano, rest[:j])
	if err != nil {
		return 0
	}
	return ts.UnixMilli()
}

func parseLine(line, device string) (model.Event, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.Contains(line, `"assistant"`) {
		return model.Event{}, false
	}
	var e entry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return model.Event{}, false
	}
	if e.Type != "assistant" {
		return model.Event{}, false
	}
	m := e.Message.Model
	if m == "" || m == "<synthetic>" {
		return model.Event{}, false
	}
	u := e.Message.Usage
	if u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheCreationTokens == 0 {
		return model.Event{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return model.Event{}, false
	}
	return model.Event{
		EventID:             eventID(&e),
		TS:                  ts.UnixMilli(),
		Device:              device,
		Source:              Source,
		Model:               m,
		Provider:            endpointProvider(e.RequestID),
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheReadTokens:     u.CacheReadTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		Cache1hTokens:       u.CacheCreation.Ephemeral1h,
		Cache5mTokens:       u.CacheCreation.Ephemeral5m,
		SessionID:           e.SessionID,
		CWD:                 e.CWD,
		GitBranch:           e.GitBranch,
		AppVersion:          e.Version,
	}, true
}

// endpointProvider reads the billing evidence off one log line (ADR-0018 §3).
//
// The first-party Anthropic endpoint returns a request id and Claude Code
// records it; a response from anywhere else leaves the field absent. That is
// the whole signal, and it is deliberately weaker than it looks:
//
//   - It settles WHICH ENDPOINT answered, not how that endpoint is paid for.
//     OAuth and API keys hit the same endpoint and get the same request id, so
//     ProviderAnthropic is as far as a log line can take us; only probing this
//     machine can split it (collect.RefineProvider).
//   - The absence verdict is terminal. A machine-level probe must never promote
//     it, because "this machine holds a subscription" and "this response came
//     from the subscription endpoint" are different claims — running both a
//     subscription and a relay from one machine is the normal case, and letting
//     the machine-level answer swallow the event-level one is precisely the bug
//     ADR-0018 exists to fix.
//   - ProviderRelay means "not first-party" and no more. A genuine Bedrock or
//     Vertex deployment answers without an Anthropic request id too and would
//     land here; that gap is recorded in ADR-0018 §4, not hidden.
//
// Measured on 855 real session files (57,885 usage events): 52,684 carry a
// request id, all of them the identical `req_011C` + 20 chars shape, and 5,201
// carry none — with the key absent rather than empty in every single case. No
// model id appears with a request id on one line and without one on another
// within the same endpoint, which is what makes the per-event test sound.
// `<synthetic>` lines never reach here: parseLine drops them earlier, and it
// must keep doing so — Claude Code writes them locally without ever sending a
// request, so counting them would sprinkle phantom relay traffic into sessions
// that never used one.
func endpointProvider(requestID string) string {
	if strings.TrimSpace(requestID) == "" {
		return model.ProviderRelay
	}
	return model.ProviderAnthropic
}

// eventID follows the community-validated dedup key (message.id + requestId):
// resumed/branched sessions copy earlier entries into new files, and both IDs
// together uniquely identify one billed API response across all copies.
func eventID(e *entry) string {
	if e.Message.ID != "" {
		return "cc:" + e.Message.ID + ":" + e.RequestID
	}
	return "cc:uuid:" + e.UUID
}
