// Package gemini parses Gemini CLI session logs (~/.gemini/tmp/**/*.json and
// *.jsonl) into unified usage events (ADR-0042).
//
// Token normalization semantics are aligned with ccusage's Rust Gemini adapter
// (adapter/gemini/parser.rs): cached overlap detection for session messages,
// cached subtraction for stats blocks, tool tokens folded into input, and
// thoughts folded into output (matching Gemini's output-rate billing and total).
package gemini

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

const Source = "gemini"

const (
	defaultModel       = "unknown"
	maxGeminiFileBytes = 64 << 20 // upper bound on a single session file read into memory
)

// MatchFile accepts *.json and *.jsonl session logs under ~/.gemini/tmp.
func MatchFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".json" || ext == ".jsonl"
}

// Parse implements collect.ParseFunc when no file path is provided.
func Parse(r io.Reader, device string, turnStartMS int64) model.ParseResult {
	return ParsePath("", r, device, turnStartMS)
}

// ParsePath reads the whole Gemini CLI session file (FullReparse) and returns
// deterministic usage events. Consumed equals the total bytes read so offset
// tracking matches file size on disk, except when a .json file fails to decode
// (e.g. mid-write truncation), where Consumed stays 0 so the next scan retries.
func ParsePath(path string, r io.Reader, device string, turnStartMS int64) (res model.ParseResult) {
	res.TurnStartMS = turnStartMS
	data, err := io.ReadAll(io.LimitReader(r, maxGeminiFileBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxGeminiFileBytes {
		return res
	}
	ext := strings.ToLower(filepath.Ext(path))
	fallbackSession := fileStem(path)
	if ext == ".jsonl" {
		res.Consumed = int64(len(data))
		res.Events = parseJSONL(data, fallbackSession, device)
		return res
	}
	if events, ok := parseJSON(data, fallbackSession, device); ok {
		res.Consumed = int64(len(data))
		res.Events = events
		return res
	}
	if ext == ".json" {
		// Truncated / mid-write .json file: leave Consumed=0 so the next scan
		// re-reads once Gemini CLI finishes writing.
		return res
	}
	// Fallback when path has no extension (e.g. direct Parse calls with JSONL).
	res.Consumed = int64(len(data))
	res.Events = parseJSONL(data, fallbackSession, device)
	return res
}

type rawRecord struct {
	Type           any              `json:"type"`
	SessionIDCamel string           `json:"sessionId"`
	SessionID      string           `json:"session_id"`
	Model          string           `json:"model"`
	ID             string           `json:"id"`
	Timestamp      any              `json:"timestamp"`
	CreatedAt      any              `json:"created_at"`
	StartTime      any              `json:"startTime"`
	LastUpdated    any              `json:"lastUpdated"`
	Messages       []rawMessage     `json:"messages"`
	Tokens         map[string]any   `json:"tokens"`
	Stats          map[string]any   `json:"stats"`
	Result         *rawResultHolder `json:"result"`
}

type rawResultHolder struct {
	Stats map[string]any `json:"stats"`
}

type rawMessage struct {
	Type      any            `json:"type"`
	ID        string         `json:"id"`
	Timestamp any            `json:"timestamp"`
	CreatedAt any            `json:"created_at"`
	Model     string         `json:"model"`
	Tokens    map[string]any `json:"tokens"`
}

func (r *rawRecord) sessionID(fallback string) string {
	if s := strings.TrimSpace(r.SessionIDCamel); s != "" {
		return s
	}
	if s := strings.TrimSpace(r.SessionID); s != "" {
		return s
	}
	if fallback != "" {
		return fallback
	}
	return "unknown"
}

func (r *rawRecord) stats() map[string]any {
	if r.Stats != nil {
		return r.Stats
	}
	if r.Result != nil && r.Result.Stats != nil {
		return r.Result.Stats
	}
	return nil
}

type geminiTokens struct {
	input    int64
	output   int64
	cached   int64
	thoughts int64
	tool     int64
	total    int64
	hasTotal bool
}

func parseJSON(data []byte, fallbackSession, device string) ([]model.Event, bool) {
	var rec rawRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	sessionID := rec.sessionID(fallbackSession)
	sessionTS := parseAnyTS(rec.StartTime)
	if sessionTS <= 0 {
		sessionTS = parseAnyTS(rec.LastUpdated)
	}
	if len(rec.Messages) > 0 {
		var events []model.Event
		directIndexes := map[string]int{}
		currentModel := strings.TrimSpace(rec.Model)
		for i, msg := range rec.Messages {
			if m := strings.TrimSpace(msg.Model); m != "" {
				currentModel = m
			}
			if !isGeminiType(msg.Type) {
				continue
			}
			toks, ok := parseTokens(msg.Tokens)
			if !ok {
				continue
			}
			modelName := strings.TrimSpace(msg.Model)
			if modelName == "" {
				modelName = currentModel
			}
			ts := parseAnyTS(msg.Timestamp)
			if ts <= 0 {
				ts = parseAnyTS(msg.CreatedAt)
			}
			if ts <= 0 {
				ts = sessionTS
			}
			msgID := strings.TrimSpace(msg.ID)
			ev, ok := buildEvent(modelName, sessionID, device, ts, i, toks, normalizeSessionInput, msgID)
			if !ok {
				continue
			}
			if msgID != "" {
				if idx, exists := directIndexes[msgID]; exists {
					events[idx] = ev
				} else {
					directIndexes[msgID] = len(events)
					events = append(events, ev)
				}
			} else {
				events = append(events, ev)
			}
		}
		return events, true
	}
	if isGeminiType(rec.Type) {
		toks, ok := parseTokens(rec.Tokens)
		if !ok {
			return nil, true
		}
		ts := firstTS(rec.Timestamp, rec.CreatedAt, rec.StartTime, rec.LastUpdated)
		if ev, ok := buildEvent(rec.Model, sessionID, device, ts, 0, toks, normalizeSessionInput, strings.TrimSpace(rec.ID)); ok {
			return []model.Event{ev}, true
		}
		return nil, true
	}
	ts := firstTS(rec.Timestamp, rec.CreatedAt, rec.StartTime, rec.LastUpdated)
	return parseStatsEvents(rec.stats(), rec.Model, sessionID, device, ts, 0), true
}

func parseJSONL(data []byte, fallbackSession, device string) []model.Event {
	sessionID := fallbackSession
	if sessionID == "" {
		sessionID = "unknown"
	}
	var currentModel string
	var events []model.Event
	directIndexes := map[string]int{}
	ordinal := 0

	for _, rawLine := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(rawLine)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal(trimmed, &rec); err != nil {
			continue
		}
		if s := strings.TrimSpace(rec.SessionIDCamel); s != "" {
			sessionID = s
		} else if s := strings.TrimSpace(rec.SessionID); s != "" {
			sessionID = s
		}
		if m := strings.TrimSpace(rec.Model); m != "" {
			currentModel = m
		}
		if isGeminiType(rec.Type) {
			toks, ok := parseTokens(rec.Tokens)
			if !ok {
				continue
			}
			modelName := strings.TrimSpace(rec.Model)
			if modelName == "" {
				modelName = currentModel
			}
			ts := firstTS(rec.Timestamp, rec.CreatedAt, rec.StartTime, rec.LastUpdated)
			msgID := strings.TrimSpace(rec.ID)
			ev, ok := buildEvent(modelName, sessionID, device, ts, ordinal, toks, normalizeSessionInput, msgID)
			if !ok {
				continue
			}
			ordinal++
			if msgID != "" {
				if idx, exists := directIndexes[msgID]; exists {
					events[idx] = ev
				} else {
					directIndexes[msgID] = len(events)
					events = append(events, ev)
				}
			} else {
				events = append(events, ev)
			}
			continue
		}
		if st := rec.stats(); st != nil {
			ts := firstTS(rec.Timestamp, rec.CreatedAt, rec.StartTime, rec.LastUpdated)
			added := parseStatsEvents(st, currentModel, sessionID, device, ts, ordinal)
			ordinal += len(added)
			events = append(events, added...)
		}
	}
	return events
}

func parseStatsEvents(stats map[string]any, modelHint, sessionID, device string, ts int64, baseOrdinal int) []model.Event {
	if stats == nil {
		return nil
	}
	if modelsRaw, ok := stats["models"].(map[string]any); ok && len(modelsRaw) > 0 {
		// Sort model keys so ordinals and event order are deterministic across runs.
		names := make([]string, 0, len(modelsRaw))
		for name := range modelsRaw {
			names = append(names, name)
		}
		sort.Strings(names)
		var events []model.Event
		for _, name := range names {
			entry, ok := modelsRaw[name].(map[string]any)
			if !ok {
				continue
			}
			tokMap, ok := entry["tokens"].(map[string]any)
			if !ok {
				continue
			}
			toks, ok := parseTokens(tokMap)
			if !ok {
				continue
			}
			if ev, ok := buildEvent(name, sessionID, device, ts, baseOrdinal+len(events), toks, subtractCachedOverlap, ""); ok {
				events = append(events, ev)
			}
		}
		if len(events) > 0 {
			return events
		}
	}
	toks, ok := parseTokens(stats)
	if !ok {
		return nil
	}
	modelName := strings.TrimSpace(modelHint)
	if modelName == "" {
		modelName = defaultModel
	}
	if ev, ok := buildEvent(modelName, sessionID, device, ts, baseOrdinal, toks, subtractCachedOverlap, ""); ok {
		return []model.Event{ev}
	}
	return nil
}

func buildEvent(
	modelName, sessionID, device string,
	tsMS int64,
	ordinal int,
	toks geminiTokens,
	normalize func(geminiTokens) (int64, int64),
	msgID string,
) (model.Event, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || tsMS <= 0 {
		return model.Event{}, false
	}
	inNoCache, cacheRead := normalize(toks)
	inputTokens := inNoCache + toks.tool
	outputTokens := toks.output + toks.thoughts
	if inputTokens == 0 && outputTokens == 0 && cacheRead == 0 {
		if toks.hasTotal && toks.total > 0 {
			outputTokens = toks.total
		} else {
			return model.Event{}, false
		}
	}
	return model.Event{
		EventID:         eventID(sessionID, msgID, tsMS, ordinal, modelName, inputTokens, outputTokens, cacheRead),
		TS:              tsMS,
		Device:          device,
		Source:          Source,
		Model:           modelName,
		Provider:        model.ProviderGoogle,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheRead,
		SessionID:       sessionID,
	}, true
}

func eventID(sessionID, msgID string, tsMS int64, ordinal int, modelName string, in, out, cr int64) string {
	var raw string
	if msgID != "" {
		raw = sessionID + "|" + msgID
	} else {
		raw = fmt.Sprintf("%s|%d|%d|%s|%d|%d|%d", sessionID, tsMS, ordinal, modelName, in, out, cr)
	}
	sum := sha1.Sum([]byte(raw))
	return "gem:" + hex.EncodeToString(sum[:12])
}

func parseTokens(m map[string]any) (geminiTokens, bool) {
	if len(m) == 0 {
		return geminiTokens{}, false
	}
	total, hasTotal := tokenValue(m, "total", "total_tokens")
	return geminiTokens{
		input:    tokenOrZero(m, "input", "prompt", "input_tokens", "prompt_tokens"),
		output:   tokenOrZero(m, "output", "candidates", "output_tokens", "candidates_tokens"),
		cached:   tokenOrZero(m, "cached", "cached_tokens"),
		thoughts: tokenOrZero(m, "thoughts", "reasoning", "thoughts_tokens", "reasoning_tokens"),
		tool:     tokenOrZero(m, "tool", "tool_tokens"),
		total:    total,
		hasTotal: hasTotal,
	}, true
}

func tokenOrZero(m map[string]any, keys ...string) int64 {
	v, _ := tokenValue(m, keys...)
	return v
}

func tokenValue(m map[string]any, keys ...string) (int64, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || raw == nil {
			continue
		}
		if f, ok := raw.(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
			if f < 0 {
				return 0, true
			}
			return int64(math.Trunc(f)), true
		}
	}
	return 0, false
}

func subtractCachedOverlap(t geminiTokens) (int64, int64) {
	cachedPortion := min(t.input, t.cached)
	return t.input - cachedPortion, t.cached
}

func normalizeSessionInput(t geminiTokens) (int64, int64) {
	inclusiveTotal := t.input + t.output + t.thoughts + t.tool
	exclusiveTotal := inclusiveTotal + t.cached
	if t.cached > 0 && t.hasTotal && t.total == inclusiveTotal && t.total != exclusiveTotal {
		return subtractCachedOverlap(t)
	}
	return t.input, t.cached
}

func isGeminiType(v any) bool {
	s, ok := v.(string)
	return ok && s == "gemini"
}

func firstTS(values ...any) int64 {
	for _, v := range values {
		if ts := parseAnyTS(v); ts > 0 {
			return ts
		}
	}
	return 0
}

func parseAnyTS(v any) int64 {
	switch tv := v.(type) {
	case float64:
		if math.IsNaN(tv) || math.IsInf(tv, 0) || tv <= 0 {
			return 0
		}
		if tv >= 1e12 {
			return int64(tv)
		}
		if tv >= 1e9 {
			return int64(tv * 1000)
		}
		return 0
	case string:
		s := strings.TrimSpace(tv)
		if s == "" {
			return 0
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UnixMilli()
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func fileStem(path string) string {
	base := filepath.Base(path)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}
