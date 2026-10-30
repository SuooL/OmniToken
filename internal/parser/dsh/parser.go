// Package dsh parses DeepSeek Harness (dsh) session logs into usage events.
//
// dsh writes one zstd-compressed JSONL file per session at
// ~/.dsh/sessions/<project>/session-<uuid>/session.jsonl.zstd. Each
// assistant/message line carries a normalized per-generation usage object
// ({inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens,
// reasoningTokens}); reasoningTokens is a subset of outputTokens, so it is not
// counted separately. Model and provider come from the request/context (or
// request/header) line that precedes the message in the same turn/step.
//
// Because the file is compressed, the whole thing is decompressed and reparsed
// on every scan (SourceSpec.FullReparse), and deterministic event IDs make the
// re-read free of double counting. The reported Consumed is the number of
// COMPRESSED bytes read, so the scan offset lines up with the file size on disk
// (ADR-0029).
//
// Generation timing comes from the timestamps dsh writes on every streamed
// chunk (ADR-0040): gen_ms runs from the final attempt's first chunk to the
// message being recorded, ttft_ms from the request start (step/start, or the
// latest llm/retry-started) to that first chunk. Failed attempts and retry
// backoff fall outside both. Only timestamps are read, never chunk payloads.
package dsh

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"

	"github.com/suool/omnitoken/internal/model"
)

// Source is the Event.Source value for dsh-observed usage.
const Source = "dsh"

// record is one JSONL line. Only the fields we read are declared; the session
// line keeps id/cwd at the top level while everything else nests under data.
type record struct {
	Type      string          `json:"type"`
	Seq       int64           `json:"seq"`
	Time      int64           `json:"time"`  // unix milliseconds
	Time0     int64           `json:"time0"` // v1 batched chunk records: first delta
	Data      json.RawMessage `json:"data"`
	ID        string          `json:"id"`        // session line
	CreatedAt int64           `json:"createdAt"` // session line
	CWD       string          `json:"cwd"`       // session line
}

type modelConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// requestHeader carries the model config under data.header.config; request/context
// carries the same fields directly under data.
type requestHeader struct {
	Header struct {
		Config modelConfig `json:"config"`
	} `json:"header"`
}

type usage struct {
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens already includes reasoningTokens, so reasoning is not read.
	OutputTokens int64 `json:"outputTokens"`
	// dsh reports non-cached input directly (inputTokens excludes cache reads),
	// so these map straight to the event's cache columns with no subtraction.
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// assistantMessage decodes usage and timing in one pass. When that fails the
// message is decoded again as usageOnly, so a shape change in the timing fields
// can only cost the timing, never the usage counts.
type assistantMessage struct {
	Usage  *usage          `json:"usage"`
	Turn   json.RawMessage `json:"turn"`
	Step   json.RawMessage `json:"step"`
	Stream []streamChunk   `json:"stream"` // v3+: the final attempt's chunks
}

type usageOnly struct {
	Usage *usage `json:"usage"`
}

// stepRef locates the LLM step a chunk, retry or message line belongs to. The
// raw values are only compared, so their JSON type never matters.
type stepRef struct {
	Turn json.RawMessage `json:"turn"`
	Step json.RawMessage `json:"step"`
}

func (s stepRef) key() string { return string(s.Turn) + "/" + string(s.Step) }

// streamChunk declares only the timestamps; chunk payloads are content and are
// never decoded.
type streamChunk struct {
	Time  int64 `json:"time"`
	Time0 int64 `json:"time0"`
}

// stepTiming is what has been seen of a step's current attempt.
type stepTiming struct {
	requestStart int64 // step/start, or the latest llm/retry-started
	firstChunk   int64 // earliest top-level chunk record (v1 layout)
}

// Parse reads a whole dsh session file (zstd-compressed JSONL) and emits one
// event per assistant/message that reports usage. r is the compressed bytes;
// Consumed is their length so the caller's offset matches the on-disk size.
func Parse(r io.Reader, device string, _ int64) (res model.ParseResult) {
	raw, err := io.ReadAll(r)
	res.Consumed = int64(len(raw))
	if err != nil {
		return res
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return res
	}
	defer dec.Close()

	br := bufio.NewReaderSize(dec, 1<<20)
	var sessionID, cwd, curProvider, curModel string
	steps := map[string]*stepTiming{}
	for {
		text, rerr := br.ReadString('\n')
		if len(text) > 0 {
			var rec record
			if json.Unmarshal([]byte(text), &rec) == nil {
				switch rec.Type {
				case "session":
					sessionID = rec.ID
					cwd = rec.CWD
				case "request/context":
					var c modelConfig
					if json.Unmarshal(rec.Data, &c) == nil {
						if c.Provider != "" {
							curProvider = c.Provider
						}
						if c.Model != "" {
							curModel = c.Model
						}
					}
				case "request/header":
					var h requestHeader
					if json.Unmarshal(rec.Data, &h) == nil {
						if h.Header.Config.Provider != "" {
							curProvider = h.Header.Config.Provider
						}
						if h.Header.Config.Model != "" {
							curModel = h.Header.Config.Model
						}
					}
				case "step/start", "llm/retry-started":
					// A new attempt: whatever the previous one streamed is
					// not part of the generation that produces the message.
					var s stepRef
					if json.Unmarshal(rec.Data, &s) == nil {
						steps[s.key()] = &stepTiming{requestStart: rec.Time}
					}
				case "assistant/chunk", "text-chunks", "reasoning-chunks", "tool-call-chunks":
					var s stepRef
					if json.Unmarshal(rec.Data, &s) == nil {
						st := steps[s.key()]
						if st == nil {
							st = &stepTiming{}
							steps[s.key()] = st
						}
						st.firstChunk = earliest(st.firstChunk, rec.Time, rec.Time0)
					}
				case "assistant/message":
					var m assistantMessage
					var genMS, ttftMS int64
					ok := json.Unmarshal(rec.Data, &m) == nil
					if ok {
						key := stepRef{Turn: m.Turn, Step: m.Step}.key()
						genMS, ttftMS = generationTiming(rec.Time, m.Stream, steps[key])
						delete(steps, key)
					} else {
						var uo usageOnly
						ok = json.Unmarshal(rec.Data, &uo) == nil
						m = assistantMessage{Usage: uo.Usage}
					}
					if ok {
						u := m.Usage
						if u != nil && (u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0) {
							res.Events = append(res.Events, model.Event{
								EventID:             eventID(sessionID, rec.Seq, u),
								DedupKey:            dedupKey(sessionID, rec.Time, u),
								TS:                  rec.Time,
								Device:              device,
								Source:              Source,
								Model:               curModel,
								Provider:            providerLabel(curProvider),
								InputTokens:         u.InputTokens,
								OutputTokens:        u.OutputTokens,
								CacheReadTokens:     u.CacheReadTokens,
								CacheCreationTokens: u.CacheWriteTokens,
								GenMS:               genMS,
								TTFTMS:              ttftMS,
								SessionID:           sessionID,
								CWD:                 cwd,
							})
						}
					}
				}
			}
		}
		if rerr != nil {
			return res
		}
	}
}

// generationTiming derives the step's gen_ms and ttft_ms (ADR-0040). The first
// chunk comes from the message's embedded stream (v3+), else from the step's
// top-level chunk records (v1). Anything not provably positive stays 0, which
// readers treat as unmeasured rather than as zero speed.
func generationTiming(msgTime int64, stream []streamChunk, st *stepTiming) (genMS, ttftMS int64) {
	var first int64
	for _, c := range stream {
		first = earliest(first, c.Time, c.Time0)
	}
	if first == 0 && st != nil {
		first = st.firstChunk
	}
	if first <= 0 {
		return 0, 0
	}
	if msgTime > first {
		genMS = msgTime - first
	}
	if st != nil && st.requestStart > 0 && first > st.requestStart {
		ttftMS = first - st.requestStart
	}
	return genMS, ttftMS
}

// earliest returns the smallest positive value among cur and ts; 0 means none.
func earliest(cur int64, ts ...int64) int64 {
	for _, t := range ts {
		if t > 0 && (cur == 0 || t < cur) {
			cur = t
		}
	}
	return cur
}

// eventID is deterministic and stable across re-reads: seq is unique within a
// session, so (session, seq) identifies one generation. Frozen once rows land
// (ADR-0004); the token fields are folded in to match the Codex id shape.
func eventID(sessionID string, seq int64, u *usage) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%d",
		sessionID, seq, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)))
	return "dsh:" + hex.EncodeToString(h[:12])
}

// dedupKey identifies the generation itself rather than the log line (ADR-0041).
// When dsh migrates a session to a newer log format it keeps the old file and
// writes a copy; the v1→v3 migration renumbered seq, so the copy gets a new
// event_id. Session, timestamp and usage survive the copy verbatim, and no two
// generations of one session share all of them. Frozen once rows land, like
// the event id. Empty without a session id or timestamp: too weak to merge on.
func dedupKey(sessionID string, ts int64, u *usage) string {
	if sessionID == "" || ts <= 0 {
		return ""
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%d",
		sessionID, ts, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)))
	return "dsg:" + hex.EncodeToString(h[:12])
}

// providerLabel keeps the provider id dsh recorded verbatim (e.g. "anthropic",
// "deepseek-official"), defaulting to unknown when absent. dsh always calls
// through a user-configured API key, so it is never a first-party subscription:
// the billing taxonomy classifies these names as official API or relay, never
// subscription (ADR-0029). No plan-evidence promotion, unlike Codex.
func providerLabel(p string) string {
	if p == "" {
		return model.ProviderUnknown
	}
	return p
}
