package antigravity

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/suool/omnitoken/internal/model"
)

// Sample based on real ~/.gemini/antigravity/brain/<uuid>/.system_generated/logs/transcript.jsonl.
// Input tokens are empirically proven to exclude cache_read_tokens (step 1: 20789+0,
// step 5: cache_read=20790 + input=4602 = 25392, step 10: cache_read=25393).
const sampleTranscript = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-23T02:18:50Z","content":"ignored prompt text"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:50Z","model":"gemini-4-neon-high","input_tokens":20789,"cache_read_tokens":0,"output_tokens":322,"thinking":"ignored","tool_calls":[{"name":"read_file"}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-23T02:18:55Z","content":"tool output"}
{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:59Z","model":"gemini-4-neon-high","input_tokens":4602,"cache_read_tokens":20790,"output_tokens":255}
{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","status":"RUNNING","created_at":"2026-11-23T02:19:01Z","model":"gemini-4-neon-high","input_tokens":100,"cache_read_tokens":0,"output_tokens":10}
{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:19:02Z","model":"gemini-4-neon-high","input_tokens":0,"cache_read_tokens":0,"output_tokens":0}
not valid json
{"step_index":10,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:19:06Z","model":"claude-sonnet-4-6","input_tokens":12200,"cache_read_tokens":25393,"cache_creation_tokens":500,"output_tokens":223}
`

func TestParsePathExtractsPlannerUsage(t *testing.T) {
	path := filepath.Join("/Users/suool/.gemini/antigravity/brain", "b8be3c72-36ad-4500-8bf9-5a456459748d", ".system_generated", "logs", "transcript.jsonl")
	res := ParsePath(path, strings.NewReader(sampleTranscript), "macbook", 0)
	if res.Consumed != int64(len(sampleTranscript)) {
		t.Fatalf("consumed = %d, want %d", res.Consumed, len(sampleTranscript))
	}
	if len(res.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(res.Events))
	}

	e0 := res.Events[0]
	if e0.Source != Source {
		t.Errorf("source = %q, want %q", e0.Source, Source)
	}
	if e0.Device != "macbook" {
		t.Errorf("device = %q, want macbook", e0.Device)
	}
	if e0.SessionID != "b8be3c72-36ad-4500-8bf9-5a456459748d" {
		t.Errorf("session_id = %q, want conversation uuid", e0.SessionID)
	}
	if e0.Model != "gemini-4-neon-high" {
		t.Errorf("model = %q", e0.Model)
	}
	if e0.Provider != model.ProviderGoogle {
		t.Errorf("provider = %q, want %q", e0.Provider, model.ProviderGoogle)
	}
	if e0.InputTokens != 20789 || e0.OutputTokens != 322 || e0.CacheReadTokens != 0 || e0.CacheCreationTokens != 0 {
		t.Errorf("e0 tokens = %+v", e0)
	}
	if e0.GenMS != 0 || e0.TTFTMS != 0 {
		t.Errorf("timing must stay unmeasured (0), got gen=%d ttft=%d", e0.GenMS, e0.TTFTMS)
	}
	if e0.DurationMS != 0 {
		t.Errorf("e0 duration_ms = %d, want 0 (same second as user_input)", e0.DurationMS)
	}

	e1 := res.Events[1]
	// InputTokens must NOT subtract CacheReadTokens: Antigravity logs already exclude cache reads.
	if e1.InputTokens != 4602 || e1.CacheReadTokens != 20790 || e1.OutputTokens != 255 {
		t.Errorf("e1 tokens = in:%d cr:%d out:%d, want 4602/20790/255", e1.InputTokens, e1.CacheReadTokens, e1.OutputTokens)
	}
	if e1.DurationMS != 4000 {
		t.Errorf("e1 duration_ms = %d, want 4000 (02:18:59 - 02:18:55)", e1.DurationMS)
	}

	e2 := res.Events[2]
	if e2.Model != "claude-sonnet-4-6" || e2.CacheCreationTokens != 500 {
		t.Errorf("e2 = %+v", e2)
	}
}

func TestPartialTrailingLineNotConsumed(t *testing.T) {
	line1 := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:50Z","model":"gemini-4-neon-high","input_tokens":100,"cache_read_tokens":0,"output_tokens":20}` + "\n"
	partial := `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:55Z","model":"gemini-4-neon-high","input_tokens":50`
	res := ParsePath("conv-1/.system_generated/logs/transcript.jsonl", strings.NewReader(line1+partial), "dev", 0)
	if res.Consumed != int64(len(line1)) {
		t.Fatalf("consumed = %d, want %d (must exclude trailing partial line)", res.Consumed, len(line1))
	}
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
}

func TestEventIDStable(t *testing.T) {
	path := "/home/u/.gemini/antigravity/brain/b8be3c72-36ad-4500-8bf9-5a456459748d/.system_generated/logs/transcript.jsonl"
	line := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:50Z","model":"gemini-4-neon-high","input_tokens":20789,"cache_read_tokens":0,"output_tokens":322}` + "\n"
	r1 := ParsePath(path, strings.NewReader(line), "mac-a", 0)
	r2 := ParsePath(path, strings.NewReader(line), "mac-b", 0)
	if len(r1.Events) != 1 || len(r2.Events) != 1 {
		t.Fatalf("expected 1 event, got %d / %d", len(r1.Events), len(r2.Events))
	}
	if r1.Events[0].EventID != r2.Events[0].EventID {
		t.Fatalf("event_id must be device-independent: %q != %q", r1.Events[0].EventID, r2.Events[0].EventID)
	}
	// Pin exact golden hash so future parser edits cannot silently drift event_id (ADR-0004).
	want := eventID("b8be3c72-36ad-4500-8bf9-5a456459748d", 1, "2026-11-23T02:18:50Z", 20789, 322, 0, 0)
	if r1.Events[0].EventID != want || !strings.HasPrefix(want, "agy:") || len(want) != 4+24 {
		t.Fatalf("event_id = %q, want %q (len 28)", r1.Events[0].EventID, want)
	}
	const golden = "agy:54ae25091176b95199d8dcea"
	if r1.Events[0].EventID != golden {
		t.Fatalf("golden event_id drifted: got %q, want %q", r1.Events[0].EventID, golden)
	}
}

func TestMatchFileFiltersOnlyCompactTranscript(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/Users/u/.gemini/antigravity/brain/uuid-1/.system_generated/logs/transcript.jsonl", true},
		{`C:\Users\u\.gemini\antigravity\brain\uuid-1\.system_generated\logs\transcript.jsonl`, true},
		{"/Users/u/.gemini/antigravity/brain/uuid-1/.system_generated/logs/transcript_full.jsonl", false},
		{"/Users/u/.gemini/antigravity/brain/uuid-1/.system_generated/logs/chunks/transcript/chunk_0.jsonl", false},
		{"/Users/u/.gemini/antigravity/brain/uuid-1/other.jsonl", false},
	}
	for _, tc := range cases {
		if got := MatchFile(tc.path); got != tc.want {
			t.Errorf("MatchFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/u/.gemini/antigravity/brain/conv-123/.system_generated/logs/transcript.jsonl", "conv-123"},
		{`C:\Users\u\.gemini\antigravity\brain\conv-win\.system_generated\logs\transcript.jsonl`, "conv-win"},
		{"/tmp/custom-conv/transcript.jsonl", "custom-conv"},
		{"transcript.jsonl", ""},
	}
	for _, tc := range cases {
		if got := sessionIDFromPath(tc.path); got != tc.want {
			t.Errorf("sessionIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestParseDirectAndEdgeCases(t *testing.T) {
	// Parse (without path) still works cleanly.
	line := `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:20:00.500Z","model":"gemini-4-neon-high","input_tokens":50,"cache_read_tokens":10,"output_tokens":5}` + "\n"
	res := Parse(strings.NewReader(line), "dev", 0)
	if len(res.Events) != 1 || res.Events[0].InputTokens != 50 {
		t.Fatalf("unexpected Parse result: %+v", res)
	}
	// Missing created_at or pending status skipped.
	bad := `{"step_index":4,"type":"PLANNER_RESPONSE","status":"PENDING","created_at":"2026-11-23T02:20:01Z","input_tokens":10}` + "\n" +
		`{"step_index":5,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"bad-ts","input_tokens":10}` + "\n"
	res2 := Parse(strings.NewReader(bad), "dev", 0)
	if len(res2.Events) != 0 {
		t.Fatalf("expected 0 events for bad/pending rows, got %d", len(res2.Events))
	}
}
