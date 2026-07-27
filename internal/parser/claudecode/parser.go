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
func Parse(r io.Reader, device string) (res model.ParseResult) {
	br := bufio.NewReaderSize(r, 1<<20)
	var prevMS int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			// Partial trailing line: leave it for the next scan.
			return res
		}
		res.Consumed += int64(len(line))
		lineMS := extractTimestampMS(line)
		if ev, ok := parseLine(line, device); ok {
			if prevMS > 0 && ev.TS > prevMS {
				ev.DurationMS = min(ev.TS-prevMS, maxSpanMS)
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
		Provider:            model.FingerprintProvider(m),
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

// eventID follows the community-validated dedup key (message.id + requestId):
// resumed/branched sessions copy earlier entries into new files, and both IDs
// together uniquely identify one billed API response across all copies.
func eventID(e *entry) string {
	if e.Message.ID != "" {
		return "cc:" + e.Message.ID + ":" + e.RequestID
	}
	return "cc:uuid:" + e.UUID
}
