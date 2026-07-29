package claudecode

import (
	"strings"
	"testing"
)

const assistantLine = `{"parentUuid":"p1","isSidechain":false,"message":{"model":"claude-fable-5","id":"msg_01","type":"message","role":"assistant","content":[],"usage":{"input_tokens":8829,"cache_creation_input_tokens":3622,"cache_read_input_tokens":15247,"output_tokens":1747,"cache_creation":{"ephemeral_1h_input_tokens":3622,"ephemeral_5m_input_tokens":0}}},"requestId":"req_01","type":"assistant","uuid":"u1","timestamp":"2026-07-19T07:40:37.441Z","cwd":"/Users/x/git/demo","sessionId":"s1","version":"2.1.201","gitBranch":"main"}`

func TestParse(t *testing.T) {
	input := assistantLine + "\n" +
		`{"type":"user","uuid":"u2","timestamp":"2026-07-19T07:41:00Z"}` + "\n" +
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":5}},"uuid":"u3","timestamp":"2026-07-19T07:42:00Z"}` + "\n" +
		`not json` + "\n" +
		`{"type":"assistant","partial line without newline`

	res := Parse(strings.NewReader(input), "dev1", 0)
	events, consumed := res.Events, res.Consumed
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	wantConsumed := int64(len(input) - len(`{"type":"assistant","partial line without newline`))
	if consumed != wantConsumed {
		t.Errorf("consumed = %d, want %d (partial line must not be consumed)", consumed, wantConsumed)
	}
	e := events[0]
	if e.EventID != "cc:msg_01:req_01" {
		t.Errorf("event id = %q", e.EventID)
	}
	if e.InputTokens != 8829 || e.OutputTokens != 1747 || e.CacheReadTokens != 15247 || e.CacheCreationTokens != 3622 {
		t.Errorf("token counts wrong: %+v", e)
	}
	if e.Cache1hTokens != 3622 {
		t.Errorf("cache 1h = %d", e.Cache1hTokens)
	}
	if e.Provider != "anthropic" || e.Source != "claude-code" || e.Device != "dev1" {
		t.Errorf("attribution wrong: %+v", e)
	}
	if e.CWD != "/Users/x/git/demo" || e.GitBranch != "main" || e.SessionID != "s1" {
		t.Errorf("context wrong: %+v", e)
	}
}

func TestProviderFingerprintViaParse(t *testing.T) {
	cases := map[string]string{
		"claude-fable-5": "anthropic",
		"us.anthropic.claude-sonnet-4-20250514-v1:0": "bedrock",
		"claude-sonnet-4@20250514":                   "vertex",
		"glm-4.7":                                    "relay",
	}
	for modelID, want := range cases {
		line := strings.Replace(assistantLine, "claude-fable-5", modelID, 1)
		events := Parse(strings.NewReader(line+"\n"), "d", 0).Events
		if len(events) != 1 {
			t.Fatalf("%s: no event", modelID)
		}
		if events[0].Provider != want {
			t.Errorf("%s: provider = %q, want %q", modelID, events[0].Provider, want)
		}
	}
}

func TestLimitHitSnapshot(t *testing.T) {
	line := `{"type":"user","uuid":"u9","timestamp":"2026-07-19T08:00:00.000Z","isApiErrorMessage":true,"message":{"role":"user","content":"Claude AI usage limit reached|1785650000"}}`
	res := Parse(strings.NewReader(line+"\n"), "dev1", 0)
	if len(res.Quotas) != 1 {
		t.Fatalf("want 1 limit-hit snapshot, got %d", len(res.Quotas))
	}
	q := res.Quotas[0]
	if q.ResetsAt != 1785650000*1000 || q.UsedPercent != 100 || q.Scope != "limit-hit" || q.Source != Source {
		t.Errorf("snapshot wrong: %+v", q)
	}
	// a normal line must not produce one
	if r2 := Parse(strings.NewReader(assistantLine+"\n"), "d", 0); len(r2.Quotas) != 0 {
		t.Errorf("normal line produced %d snapshots", len(r2.Quotas))
	}
}

// gen_ms measures [request sent → response recorded], so a user line inside
// the same chunk must set the interval (ADR-0009).
func TestParseGenMSFromUserLine(t *testing.T) {
	input := `{"type":"user","uuid":"u0","timestamp":"2026-07-19T07:40:07.441Z"}` + "\n" +
		assistantLine + "\n"

	res := Parse(strings.NewReader(input), "dev1", 0)
	if len(res.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(res.Events))
	}
	// 07:40:07.441 → 07:40:37.441 is 30s.
	if got := res.Events[0].GenMS; got != 30_000 {
		t.Errorf("gen_ms = %d, want 30000", got)
	}
}

// The turn's user line usually landed in an earlier scan: a five-second poll
// almost never captures both ends of a ten-second turn. Without the carry the
// live path yields gen_ms=0 for nearly every event — measured at 0 of 25 over
// ten minutes before this was added — which is precisely the case the metric
// exists to serve.
func TestParseCarriesTurnStartAcrossChunks(t *testing.T) {
	first := `{"type":"user","uuid":"u0","timestamp":"2026-07-19T07:40:07.441Z"}` + "\n"
	res := Parse(strings.NewReader(first), "dev1", 0)
	if len(res.Events) != 0 {
		t.Fatalf("user-only chunk produced %d events", len(res.Events))
	}
	if res.TurnStartMS == 0 {
		t.Fatal("TurnStartMS not carried out of a chunk holding only the user line")
	}

	// Next scan sees only the assistant line, and must still measure 30s.
	res2 := Parse(strings.NewReader(assistantLine+"\n"), "dev1", res.TurnStartMS)
	if len(res2.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(res2.Events))
	}
	if got := res2.Events[0].GenMS; got != 30_000 {
		t.Errorf("gen_ms = %d across chunks, want 30000", got)
	}
	if res2.TurnStartMS != res.TurnStartMS {
		t.Errorf("carry changed to %d without a new user line, want %d",
			res2.TurnStartMS, res.TurnStartMS)
	}
}

// A turn still in flight must not be measured from a stale start once the next
// one begins: the newest user line wins.
func TestParseTurnStartAdvancesToLatestUserLine(t *testing.T) {
	input := `{"type":"user","uuid":"u0","timestamp":"2026-07-19T07:00:00.000Z"}` + "\n" +
		`{"type":"user","uuid":"u1","timestamp":"2026-07-19T07:40:27.441Z"}` + "\n" +
		assistantLine + "\n"

	res := Parse(strings.NewReader(input), "dev1", 0)
	if got := res.Events[0].GenMS; got != 10_000 {
		t.Errorf("gen_ms = %d, want 10000 (measured from the later user line)", got)
	}
}
