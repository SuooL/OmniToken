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

	res := Parse(strings.NewReader(input), "dev1")
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
		events := Parse(strings.NewReader(line+"\n"), "d").Events
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
	res := Parse(strings.NewReader(line+"\n"), "dev1")
	if len(res.Quotas) != 1 {
		t.Fatalf("want 1 limit-hit snapshot, got %d", len(res.Quotas))
	}
	q := res.Quotas[0]
	if q.ResetsAt != 1785650000*1000 || q.UsedPercent != 100 || q.Scope != "limit-hit" || q.Source != Source {
		t.Errorf("snapshot wrong: %+v", q)
	}
	// a normal line must not produce one
	if r2 := Parse(strings.NewReader(assistantLine+"\n"), "d"); len(r2.Quotas) != 0 {
		t.Errorf("normal line produced %d snapshots", len(r2.Quotas))
	}
}
