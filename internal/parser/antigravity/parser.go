// Package antigravity parses Google Antigravity transcript JSONL logs
// (~/.gemini/antigravity/brain/<conversation-id>/.system_generated/logs/transcript.jsonl)
// into unified usage events (ADR-0042).
//
// Only metadata and token count fields are deserialized; prompt text, thinking
// traces, tool payloads, and media references are never read (N4).
package antigravity

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

const Source = "antigravity"

// maxSpanMS clamps work-time inter-step gaps (ADR-0006).
const maxSpanMS = 30 * 60 * 1000

// transcriptLine declares only metadata and token fields; conversation bodies
// are discarded at the JSON decoder boundary.
type transcriptLine struct {
	StepIndex           int64  `json:"step_index"`
	Type                string `json:"type"`
	Status              string `json:"status"`
	CreatedAt           string `json:"created_at"`
	Model               string `json:"model"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
}

// MatchFile accepts only compact transcript.jsonl files under
// .system_generated/logs/, excluding transcript_full.jsonl and chunks/ copies.
func MatchFile(path string) bool {
	parts := splitPath(path)
	n := len(parts)
	if n < 3 {
		return false
	}
	return parts[n-1] == "transcript.jsonl" &&
		parts[n-2] == "logs" &&
		parts[n-3] == ".system_generated"
}

// Parse implements collect.ParseFunc when no file path is provided.
func Parse(r io.Reader, device string, turnStartMS int64) model.ParseResult {
	return ParsePath("", r, device, turnStartMS)
}

// ParsePath incrementally consumes complete JSONL lines from r, attributing
// events to device and deriving session_id from the transcript file path.
// Trailing partial lines (without '\n') are not consumed so the next scan
// re-reads them once flushed.
func ParsePath(path string, r io.Reader, device string, turnStartMS int64) (res model.ParseResult) {
	sessionID := sessionIDFromPath(path)
	br := bufio.NewReaderSize(r, 1<<20)
	var prevMS int64
	res.TurnStartMS = turnStartMS
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return res
		}
		res.Consumed += int64(len(line))
		rec, ok := decodeLine(line)
		if !ok {
			continue
		}
		lineMS := parseTimestampMS(rec.CreatedAt)
		if ev, ok := buildEvent(rec, sessionID, device, lineMS); ok {
			if prevMS > 0 && ev.TS > prevMS {
				ev.DurationMS = min(ev.TS-prevMS, maxSpanMS)
			}
			res.Events = append(res.Events, ev)
		}
		if lineMS > 0 {
			prevMS = lineMS
		}
	}
}

func decodeLine(line string) (transcriptLine, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '{' {
		return transcriptLine{}, false
	}
	var rec transcriptLine
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return transcriptLine{}, false
	}
	return rec, true
}

func buildEvent(rec transcriptLine, sessionID, device string, tsMS int64) (model.Event, bool) {
	if rec.Type != "PLANNER_RESPONSE" {
		return model.Event{}, false
	}
	if rec.Status == "RUNNING" || rec.Status == "PENDING" {
		return model.Event{}, false
	}
	if tsMS <= 0 {
		return model.Event{}, false
	}
	in := max(rec.InputTokens, 0)
	out := max(rec.OutputTokens, 0)
	cr := max(rec.CacheReadTokens, 0)
	cc := max(rec.CacheCreationTokens, 0)
	if in+out+cr+cc == 0 {
		return model.Event{}, false
	}
	return model.Event{
		EventID:             eventID(sessionID, rec.StepIndex, rec.CreatedAt, in, out, cr, cc),
		TS:                  tsMS,
		Device:              device,
		Source:              Source,
		Model:               strings.TrimSpace(rec.Model),
		Provider:            model.ProviderGoogle,
		InputTokens:         in,
		OutputTokens:        out,
		CacheReadTokens:     cr,
		CacheCreationTokens: cc,
		SessionID:           sessionID,
	}, true
}

func eventID(sessionID string, stepIndex int64, createdAt string, in, out, cr, cc int64) string {
	raw := fmt.Sprintf("%s|%d|%s|%d|%d|%d|%d", sessionID, stepIndex, createdAt, in, out, cr, cc)
	sum := sha1.Sum([]byte(raw))
	return "agy:" + hex.EncodeToString(sum[:12])
}

func sessionIDFromPath(path string) string {
	parts := splitPath(path)
	n := len(parts)
	if n >= 4 && parts[n-1] == "transcript.jsonl" && parts[n-2] == "logs" && parts[n-3] == ".system_generated" {
		return parts[n-4]
	}
	if n >= 2 {
		return parts[n-2]
	}
	return ""
}

func splitPath(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
}

func parseTimestampMS(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UnixMilli()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UnixMilli()
	}
	return 0
}
