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

// Fixtures for generation timing (ADR-0040). Shapes follow real logs: v3/v4
// embed the final attempt's chunks in assistant/message.data.stream, either as
// single chunks ({type:"chunk", time}) or batched deltas ({time0, dt}); v1
// writes the same chunks as top-level records keyed by turn/step.
const (
	stepStart = `{"type":"step/start","seq":40,"time":1787403610000,"data":{"turn":1,"step":1}}`
	// First chunk at 1787403612000: TTFT 2000ms after step/start, generation
	// 6284ms until the message is recorded at 1787403618284.
	msgStream = `{"type":"assistant/message","seq":42,"time":1787403618284,"data":{"turn":1,"step":1,"message":"hi","stream":[` +
		`{"type":"chunk","time":1787403612000,"chunk":{"type":"block-start","index":0,"blockType":"text"}},` +
		`{"type":"text-chunks","time0":1787403612010,"index":0,"dt":[0,5,7],"texts":["a","b","c"]},` +
		`{"type":"chunk","time":1787403618280,"chunk":{"type":"finish","reason":"stop"}}],` +
		`"usage":{"inputTokens":2,"outputTokens":273,"cacheReadTokens":100,"cacheWriteTokens":18745,"reasoningTokens":90}}}`
)

func TestGenMSFromEmbeddedStream(t *testing.T) {
	res := parse(t, sessionLine, reqCtxAnth, stepStart, msgStream)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	e := res.Events[0]
	if e.GenMS != 6284 {
		t.Errorf("gen_ms = %d, want 6284 (first streamed chunk -> message recorded)", e.GenMS)
	}
	if e.TTFTMS != 2000 {
		t.Errorf("ttft_ms = %d, want 2000 (step/start -> first streamed chunk)", e.TTFTMS)
	}
}

func TestGenMSIgnoresFailedAttempts(t *testing.T) {
	// A failed attempt streamed chunks earlier in the step; dsh logged it as
	// assistant/attempt, waited, and retried. Only the final attempt is
	// generation: neither the failed stream nor the backoff may widen the
	// interval, and TTFT restarts at llm/retry-started.
	attempt := `{"type":"assistant/attempt","seq":41,"time":1787403611000,"data":{"turn":1,"step":1,"stream":[{"type":"chunk","time":1787403610500,"chunk":{"type":"block-start"}}]}}`
	retry := `{"type":"llm/retry-started","seq":41,"time":1787403611500,"data":{"turn":1,"step":1,"retry":1,"retryId":"r1"}}`
	res := parse(t, sessionLine, reqCtxAnth, stepStart, attempt, retry, msgStream)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	if e := res.Events[0]; e.GenMS != 6284 || e.TTFTMS != 500 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want 6284, 500 (final attempt only)", e.GenMS, e.TTFTMS)
	}
}

func TestGenMSFromV1ChunkRecords(t *testing.T) {
	// v1 has no embedded stream: chunks are top-level records for the step.
	// A failed attempt's chunk precedes llm/retry-started and must be dropped;
	// a chunk of another step must not leak into this one.
	failed := `{"type":"assistant/chunk","seq":41,"time":1787403610400,"data":{"turn":1,"step":1,"chunk":{"type":"block-start"}}}`
	retry := `{"type":"llm/retry-started","seq":42,"time":1787403611000,"data":{"turn":1,"step":1,"retry":1,"retryId":"r1"}}`
	other := `{"type":"assistant/chunk","seq":43,"time":1787403611100,"data":{"turn":1,"step":2,"chunk":{"type":"block-start"}}}`
	first := `{"type":"assistant/chunk","seq":44,"time":1787403611200,"data":{"turn":1,"step":1,"chunk":{"type":"block-start"}}}`
	deltas := `{"type":"text-chunks","seq0":45,"time0":1787403611210,"data":{"turn":1,"step":1,"index":0,"dt":[0,4],"texts":["x","y"]}}`
	msg := `{"type":"assistant/message","seq":50,"time":1787403615200,"data":{"turn":1,"step":1,"message":"hi","usage":{"inputTokens":2,"outputTokens":400}}}`
	res := parse(t, sessionLine, reqCtxAnth, stepStart, failed, retry, other, first, deltas, msg)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	if e := res.Events[0]; e.GenMS != 4000 || e.TTFTMS != 200 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want 4000, 200", e.GenMS, e.TTFTMS)
	}
}

func TestV1ChunksStartedBeforeDeltas(t *testing.T) {
	// Batched deltas can be the first record of a step; time0 then marks the
	// first token.
	deltas := `{"type":"reasoning-chunks","seq0":41,"time0":1787403611000,"data":{"turn":1,"step":1,"index":0,"dt":[0,4],"texts":["x","y"]}}`
	msg := `{"type":"assistant/message","seq":50,"time":1787403615000,"data":{"turn":1,"step":1,"usage":{"inputTokens":2,"outputTokens":400}}}`
	res := parse(t, sessionLine, reqCtxAnth, stepStart, deltas, msg)
	if e := res.Events[0]; e.GenMS != 4000 || e.TTFTMS != 1000 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want 4000, 1000", e.GenMS, e.TTFTMS)
	}
}

func TestNoTimingLeavesSpeedFieldsUnset(t *testing.T) {
	// No chunk timestamps at all: the row is unmeasured, never "speed zero".
	res := parse(t, sessionLine, reqCtxAnth, stepStart, msgAnth)
	if e := res.Events[0]; e.GenMS != 0 || e.TTFTMS != 0 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want both 0 without chunk timing", e.GenMS, e.TTFTMS)
	}
}

func TestNonPositiveIntervalIsUnmeasured(t *testing.T) {
	// A first chunk at or after the message time cannot be a generation
	// interval; emit nothing rather than a zero or negative duration.
	msg := `{"type":"assistant/message","seq":42,"time":1787403612000,"data":{"turn":1,"step":1,"stream":[{"type":"chunk","time":1787403612000}],"usage":{"inputTokens":2,"outputTokens":1}}}`
	res := parse(t, sessionLine, reqCtxAnth, stepStart, msg)
	if e := res.Events[0]; e.GenMS != 0 {
		t.Errorf("gen_ms = %d, want 0 for a non-positive interval", e.GenMS)
	}
}

func TestTTFTNeedsRequestStart(t *testing.T) {
	// Without step/start (or retry) for the step there is no request start, so
	// TTFT is unknown while generation time is still measurable.
	res := parse(t, sessionLine, reqCtxAnth, msgStream)
	if e := res.Events[0]; e.GenMS != 6284 || e.TTFTMS != 0 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want 6284, 0", e.GenMS, e.TTFTMS)
	}
}

func TestMalformedTimingNeverCostsTheCounts(t *testing.T) {
	// A timestamp of an unexpected type must cost only the timing: the usage
	// row is still emitted with the frozen id.
	msg := `{"type":"assistant/message","seq":42,"time":1787403618284,"data":{"turn":1,"step":1,"stream":[{"type":"chunk","time":"soon"}],` +
		`"usage":{"inputTokens":2,"outputTokens":273,"cacheReadTokens":100,"cacheWriteTokens":18745}}}`
	res := parse(t, sessionLine, reqCtxAnth, stepStart, msg)
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1 (usage kept despite bad timing)", len(res.Events))
	}
	e := res.Events[0]
	if e.OutputTokens != 273 || e.EventID != "dsh:30bb9694b91fb2880e026a1e" {
		t.Errorf("counts or id changed: out=%d id=%q", e.OutputTokens, e.EventID)
	}
	if e.GenMS != 0 || e.TTFTMS != 0 {
		t.Errorf("gen_ms = %d, ttft_ms = %d; want 0 when timing cannot be read", e.GenMS, e.TTFTMS)
	}
}

func TestTimingDoesNotChangeEventID(t *testing.T) {
	// gen_ms/ttft_ms are derived columns (ADR-0009): the same generation must
	// keep the frozen id whether or not its stream timing is read.
	plain := parse(t, sessionLine, reqCtxAnth, msgAnth).Events[0].EventID
	timed := parse(t, sessionLine, reqCtxAnth, stepStart, msgStream).Events[0].EventID
	if plain != timed || timed != "dsh:30bb9694b91fb2880e026a1e" {
		t.Fatalf("event id changed with timing: plain %q, timed %q", plain, timed)
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
