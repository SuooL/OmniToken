package dsh

import (
	"bytes"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/suool/omnitoken/internal/model"
)

// zstdJSONL compresses newline-joined JSONL the way dsh stores session.jsonl.zstd,
// so tests exercise the real decompression path Parse takes.
func zstdJSONL(t *testing.T, lines ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func parse(t *testing.T, lines ...string) model.ParseResult {
	t.Helper()
	comp := zstdJSONL(t, lines...)
	res := Parse(bytes.NewReader(comp), "dev-1", 0)
	if res.Consumed != int64(len(comp)) {
		t.Fatalf("Consumed = %d, want compressed length %d", res.Consumed, len(comp))
	}
	return res
}

const (
	sessionLine = `{"type":"session","id":"session-abc","createdAt":1787403597075,"cwd":"/Users/x/git/proj","delegationDepth":0}`
	reqCtxAnth  = `{"type":"request/context","seq":13,"time":1787403610543,"data":{"provider":"anthropic","model":"claude-opus-4-8","contextWindow":1000000}}`
	msgAnth     = `{"type":"assistant/message","seq":42,"time":1787403618284,"data":{"turn":1,"step":1,"message":"hi","usage":{"inputTokens":2,"outputTokens":273,"cacheReadTokens":100,"cacheWriteTokens":18745,"reasoningTokens":90}}}`
)

func TestParseBasic(t *testing.T) {
	res := parse(t, sessionLine, reqCtxAnth, msgAnth)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	e := res.Events[0]
	if e.Source != "dsh" {
		t.Errorf("source = %q, want dsh", e.Source)
	}
	if e.Device != "dev-1" || e.Model != "claude-opus-4-8" || e.Provider != "anthropic" {
		t.Errorf("attribution wrong: %+v", e)
	}
	if e.SessionID != "session-abc" || e.CWD != "/Users/x/git/proj" || e.TS != 1787403618284 {
		t.Errorf("session/cwd/ts wrong: %+v", e)
	}
	// dsh reports non-cached input directly; no subtraction. reasoning is a
	// subset of output and must not be added on.
	if e.InputTokens != 2 || e.OutputTokens != 273 || e.CacheReadTokens != 100 || e.CacheCreationTokens != 18745 {
		t.Errorf("token split wrong: in=%d out=%d cr=%d cc=%d", e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens)
	}
	if e.DedupKey != "" {
		t.Errorf("dsh must not set a dedup key, got %q", e.DedupKey)
	}
}

func TestModelAndProviderTrackAcrossSteps(t *testing.T) {
	// A session that switches provider/model mid-way (two request/context lines).
	reqCtxDeep := `{"type":"request/context","seq":60,"time":1787403700000,"data":{"provider":"deepseek-official","model":"deepseek-v4-pro"}}`
	msgDeep := `{"type":"assistant/message","seq":70,"time":1787403701000,"data":{"usage":{"inputTokens":5,"outputTokens":900}}}`
	res := parse(t, sessionLine, reqCtxAnth, msgAnth, reqCtxDeep, msgDeep)
	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(res.Events))
	}
	if res.Events[0].Model != "claude-opus-4-8" || res.Events[0].Provider != "anthropic" {
		t.Errorf("first event kept wrong model/provider: %+v", res.Events[0])
	}
	if res.Events[1].Model != "deepseek-v4-pro" || res.Events[1].Provider != "deepseek-official" {
		t.Errorf("second event did not pick up the switched model/provider: %+v", res.Events[1])
	}
}

func TestRequestHeaderAlsoSetsModel(t *testing.T) {
	// Some turns carry the config under request/header.data.header.config.
	hdr := `{"type":"request/header","seq":12,"time":1787403610542,"data":{"header":{"config":{"provider":"openai","model":"gpt-5.6-sol"}}}}`
	msg := `{"type":"assistant/message","seq":20,"time":1787403611000,"data":{"usage":{"inputTokens":1,"outputTokens":50}}}`
	res := parse(t, sessionLine, hdr, msg)
	if len(res.Events) != 1 || res.Events[0].Model != "gpt-5.6-sol" || res.Events[0].Provider != "openai" {
		t.Fatalf("request/header not applied: %+v", res.Events)
	}
}

func TestSkipsMessagesWithoutUsageOrAllZero(t *testing.T) {
	noUsage := `{"type":"assistant/message","seq":50,"time":1787403619000,"data":{"turn":1,"message":"no usage here"}}`
	zeroUsage := `{"type":"assistant/message","seq":51,"time":1787403619500,"data":{"usage":{"inputTokens":0,"outputTokens":0}}}`
	// Non-message noise that must never produce events.
	noise := `{"type":"assistant/chunk","seq":15,"time":1787403613063,"data":{"chunk":{"type":"block-start"}}}`
	res := parse(t, sessionLine, reqCtxAnth, noise, noUsage, zeroUsage)
	if len(res.Events) != 0 {
		t.Fatalf("got %d events, want 0 (no usable usage): %+v", len(res.Events), res.Events)
	}
}

func TestMissingProviderIsUnknown(t *testing.T) {
	// A message before any request/context: model empty, provider unknown.
	msg := `{"type":"assistant/message","seq":9,"time":1787403605000,"data":{"usage":{"inputTokens":3,"outputTokens":7}}}`
	res := parse(t, sessionLine, msg)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	if res.Events[0].Provider != model.ProviderUnknown {
		t.Errorf("provider = %q, want %q", res.Events[0].Provider, model.ProviderUnknown)
	}
	if res.Events[0].Model != "" {
		t.Errorf("model = %q, want empty", res.Events[0].Model)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	res := parse(t, sessionLine, "not json at all", reqCtxAnth, `{"type":"assistant/message","data":{`, msgAnth)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1 (malformed lines ignored)", len(res.Events))
	}
}

// The event id is frozen once rows land (ADR-0004). This pins the exact string
// so a future refactor cannot silently change it and double-count history.
func TestEventIDStable(t *testing.T) {
	// Frozen forever once history is ingested (ADR-0004): a change here means
	// re-scanned sessions would double-count. Recompute deliberately, never
	// casually.
	const want = "dsh:30bb9694b91fb2880e026a1e"
	res := parse(t, sessionLine, reqCtxAnth, msgAnth)
	if got := res.Events[0].EventID; got != want {
		t.Fatalf("event id = %q, want %q (changing it double-counts history)", got, want)
	}
}

func TestEventIDDistinctPerGeneration(t *testing.T) {
	msg2 := `{"type":"assistant/message","seq":43,"time":1787403620000,"data":{"usage":{"inputTokens":2,"outputTokens":400}}}`
	res := parse(t, sessionLine, reqCtxAnth, msgAnth, msg2)
	if len(res.Events) != 2 {
		t.Fatalf("want 2 events")
	}
	if res.Events[0].EventID == res.Events[1].EventID {
		t.Fatalf("distinct generations share an event id: %q", res.Events[0].EventID)
	}
}

func TestGarbageInputDoesNotPanic(t *testing.T) {
	// Not a zstd stream at all: Parse must return cleanly (Consumed set, no events).
	res := Parse(bytes.NewReader([]byte("plain uncompressed bytes")), "dev-1", 0)
	if res.Consumed != int64(len("plain uncompressed bytes")) {
		t.Errorf("Consumed = %d, want %d", res.Consumed, len("plain uncompressed bytes"))
	}
	if len(res.Events) != 0 {
		t.Errorf("got %d events from non-zstd input, want 0", len(res.Events))
	}
}
