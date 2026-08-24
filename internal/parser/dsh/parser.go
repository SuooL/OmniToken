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
	Time      int64           `json:"time"` // unix milliseconds
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

type assistantMessage struct {
	Usage *usage `json:"usage"`
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
				case "assistant/message":
					var m assistantMessage
					if json.Unmarshal(rec.Data, &m) == nil && m.Usage != nil {
						u := m.Usage
						if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 {
							res.Events = append(res.Events, model.Event{
								EventID:             eventID(sessionID, rec.Seq, u),
								TS:                  rec.Time,
								Device:              device,
								Source:              Source,
								Model:               curModel,
								Provider:            providerLabel(curProvider),
								InputTokens:         u.InputTokens,
								OutputTokens:        u.OutputTokens,
								CacheReadTokens:     u.CacheReadTokens,
								CacheCreationTokens: u.CacheWriteTokens,
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

// eventID is deterministic and stable across re-reads: seq is unique within a
// session, so (session, seq) identifies one generation. Frozen once rows land
// (ADR-0004); the token fields are folded in to match the Codex id shape.
func eventID(sessionID string, seq int64, u *usage) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d|%d",
		sessionID, seq, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)))
	return "dsh:" + hex.EncodeToString(h[:12])
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
