package gemini

import (
	"strings"
	"testing"

	"github.com/suool/omnitoken/internal/model"
)

// Mirrors ccusage adapter/gemini/loader.rs real sample:
// input=15327 includes cached=11526 because total=16276 == 15327+23+919+7.
const sampleSessionJSON = `{
  "sessionId": "sess-gem-001",
  "startTime": "2026-05-17T11:07:30.000Z",
  "lastUpdated": "2026-05-17T11:08:00.000Z",
  "messages": [
    {
      "id": "msg-user",
      "timestamp": "2026-05-17T11:07:30.000Z",
      "type": "user",
      "content": "hello"
    },
    {
      "id": "msg-a",
      "timestamp": "2026-05-17T11:07:32.000Z",
      "type": "gemini",
      "model": "gemini-3-flash-preview",
      "tokens": {
        "input": 15327,
        "output": 23,
        "cached": 11526,
        "thoughts": 919,
        "tool": 7,
        "total": 16276
      }
    },
    {
      "id": "msg-b",
      "timestamp": "2026-05-17T11:07:45.000Z",
      "type": "gemini",
      "model": "gemini-2.5-pro",
      "tokens": {
        "input": 500,
        "output": 100,
        "cached": 400,
        "thoughts": 50,
        "tool": 0,
        "total": 1050
      }
    }
  ]
}`

func TestParseSessionJSONTokenSemantics(t *testing.T) {
	path := "/home/u/.gemini/tmp/proj-hash/chats/session-2026-05-17.json"
	res := ParsePath(path, strings.NewReader(sampleSessionJSON), "linux-box", 0)
	if res.Consumed != int64(len(sampleSessionJSON)) {
		t.Fatalf("consumed = %d, want %d", res.Consumed, len(sampleSessionJSON))
	}
	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(res.Events))
	}

	// msg-a: input(15327) includes cached(11526) because 15327+23+919+7 == 16276.
	// non-cached input = (15327 - 11526) + tool(7) = 3808
	// cache_read = 11526
	// output = output(23) + thoughts(919) = 942
	// sum = 3808 + 11526 + 942 = 16276 == total!
	e0 := res.Events[0]
	if e0.Source != Source || e0.Provider != model.ProviderGoogle {
		t.Errorf("source/provider = %q/%q", e0.Source, e0.Provider)
	}
	if e0.SessionID != "sess-gem-001" || e0.Model != "gemini-3-flash-preview" {
		t.Errorf("session/model = %q/%q", e0.SessionID, e0.Model)
	}
	if e0.InputTokens != 3808 || e0.CacheReadTokens != 11526 || e0.OutputTokens != 942 {
		t.Errorf("e0 tokens = in:%d cr:%d out:%d, want 3808/11526/942", e0.InputTokens, e0.CacheReadTokens, e0.OutputTokens)
	}
	if e0.InputTokens+e0.CacheReadTokens+e0.OutputTokens != 16276 {
		t.Errorf("e0 sum = %d, want 16276", e0.InputTokens+e0.CacheReadTokens+e0.OutputTokens)
	}

	// msg-b: total(1050) == input(500)+output(100)+thoughts(50)+cached(400) -> input already excludes cached.
	e1 := res.Events[1]
	if e1.InputTokens != 500 || e1.CacheReadTokens != 400 || e1.OutputTokens != 150 {
		t.Errorf("e1 tokens = in:%d cr:%d out:%d, want 500/400/150", e1.InputTokens, e1.CacheReadTokens, e1.OutputTokens)
	}
}

func TestParseJSONLDedupAndStats(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"sessionId":"sess-jsonl","model":"gemini-2.5-flash","id":"m1","timestamp":"2026-05-17T12:00:00Z","type":"gemini","tokens":{"input":100,"output":10}}`,
		`{"sessionId":"sess-jsonl","model":"gemini-2.5-flash","id":"m1","timestamp":"2026-05-17T12:00:01Z","type":"gemini","tokens":{"input":100,"output":55,"thoughts":5}}`,
		`{"timestamp":"2026-05-17T12:01:00Z","result":{"stats":{"models":{"gemini-2.5-pro":{"tokens":{"prompt":1000,"cached":600,"candidates":200,"reasoning":50}}}}}}`,
		`{"timestamp":"2026-05-17T12:02:00Z","type":"gemini","model":"gemini-test","tokens":{"total":654}}`,
		`{"type":" gemini ","timestamp":"2026-05-17T12:03:00Z","stats":{"input_tokens":300,"cached_tokens":100,"output_tokens":40}}`,
	}, "\n") + "\n"

	res := ParsePath("/home/u/.gemini/tmp/a/log.jsonl", strings.NewReader(jsonl), "dev", 0)
	if len(res.Events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(res.Events), res.Events)
	}
	// Duplicate id "m1" should retain the latest revision (output 55+5=60).
	if res.Events[0].OutputTokens != 60 || res.Events[0].InputTokens != 100 {
		t.Errorf("dedup m1 = %+v", res.Events[0])
	}
	// stats.models subtracts cached overlap: 1000-600=400 input, 600 cache_read, 250 output.
	if res.Events[1].Model != "gemini-2.5-pro" || res.Events[1].InputTokens != 400 || res.Events[1].CacheReadTokens != 600 || res.Events[1].OutputTokens != 250 {
		t.Errorf("stats model = %+v", res.Events[1])
	}
	// Total-only fallback goes to OutputTokens (matching ccusage).
	if res.Events[2].OutputTokens != 654 || res.Events[2].InputTokens != 0 {
		t.Errorf("total fallback = %+v", res.Events[2])
	}
	// Flat stats inherits current model ("gemini-test") and subtracts cached overlap.
	if res.Events[3].Model != "gemini-test" || res.Events[3].InputTokens != 200 || res.Events[3].CacheReadTokens != 100 || res.Events[3].OutputTokens != 40 {
		t.Errorf("flat stats = %+v", res.Events[3])
	}
}

func TestParseTopLevelSingleRecordAndStatsJSON(t *testing.T) {
	single := `{"session_id":"s-single","id":"m9","timestamp":"2026-05-17T13:00:00Z","type":"gemini","model":"gemini-2.5-flash","tokens":{"input":80,"output":20}}`
	res := ParsePath("/tmp/chats/s-single.json", strings.NewReader(single), "dev", 0)
	if len(res.Events) != 1 || res.Events[0].InputTokens != 80 || res.Events[0].OutputTokens != 20 {
		t.Fatalf("single json = %+v", res.Events)
	}

	statsOnly := `{"timestamp":"2026-05-17T13:05:00Z","stats":{"models":{"gemini-2.5-flash":{"tokens":{"input":500,"cached":200,"output":50}}}}}`
	res2 := ParsePath("/tmp/chats/run-1.json", strings.NewReader(statsOnly), "dev", 0)
	if len(res2.Events) != 1 || res2.Events[0].SessionID != "run-1" || res2.Events[0].InputTokens != 300 {
		t.Fatalf("stats json = %+v", res2.Events)
	}
}

func TestEventIDStable(t *testing.T) {
	path := "/home/u/.gemini/tmp/proj/chats/session-1.json"
	r1 := ParsePath(path, strings.NewReader(sampleSessionJSON), "dev-a", 0)
	r2 := ParsePath(path, strings.NewReader(sampleSessionJSON), "dev-b", 0)
	if len(r1.Events) != 2 || r1.Events[0].EventID != r2.Events[0].EventID {
		t.Fatalf("event_id not stable across devices: %+v vs %+v", r1.Events, r2.Events)
	}
	const golden = "gem:c69e9ece71375265bdc63f8f"
	if r1.Events[0].EventID != golden {
		t.Fatalf("golden event_id drifted: got %q, want %q", r1.Events[0].EventID, golden)
	}
}

func TestEdgeCasesAndDirectParse(t *testing.T) {
	// Empty input -> 0 events, 0 consumed.
	if res := Parse(strings.NewReader(""), "dev", 0); res.Consumed != 0 || len(res.Events) != 0 {
		t.Fatalf("empty parse = %+v", res)
	}
	// JSONL passed to direct Parse() (no extension) falls back to parseJSONL.
	jsonl := `{"session_id":"s2","type":"gemini","model":"gemini-2.5-pro","created_at":"2026-05-17T14:00:00Z","tokens":{"input":-5,"output":10}}` + "\n" +
		`not json` + "\n" +
		`{"type":"gemini","model":"gemini-2.5-pro","timestamp":"2026-05-17T14:01:00Z","tokens":{}}` + "\n" +
		`{"type":"gemini","model":"gemini-2.5-pro","timestamp":"2026-05-17T14:02:00Z","tokens":{"input":0,"output":0}}` + "\n" +
		`{"type":5,"timestamp":"2026-05-17T14:03:00Z","stats":{"input":10,"output":2}}` + "\n"
	res := Parse(strings.NewReader(jsonl), "dev", 0)
	if len(res.Events) != 2 {
		t.Fatalf("direct jsonl got %d events, want 2: %+v", len(res.Events), res.Events)
	}
	if res.Events[0].InputTokens != 0 || res.Events[0].OutputTokens != 10 {
		t.Errorf("clamped negative input = %+v", res.Events[0])
	}
	// Session JSON using lastUpdated fallback and message without timestamp.
	sess := `{"lastUpdated":"2026-05-17T15:00:00Z","messages":[{"type":"gemini","model":"gemini-2.5-flash","tokens":{"input":10,"output":5}}]}`
	res2 := ParsePath("/tmp/custom-sess.json", strings.NewReader(sess), "dev", 0)
	if len(res2.Events) != 1 || res2.Events[0].SessionID != "custom-sess" {
		t.Fatalf("lastUpdated fallback = %+v", res2.Events)
	}
	// Invalid single-record JSON without tokens or missing timestamp returns 0 events.
	bad := `{"type":"gemini","model":"gemini-2.5-flash"}`
	if res3 := ParsePath("/tmp/bad.json", strings.NewReader(bad), "dev", 0); len(res3.Events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(res3.Events))
	}
	if res4 := ParsePath("/tmp/not-ts.json", strings.NewReader(`{"type":"gemini","model":"m","timestamp":"bad","tokens":{"input":1}}`), "dev", 0); len(res4.Events) != 0 {
		t.Fatalf("expected 0 events for bad ts, got %d", len(res4.Events))
	}
}

func TestMatchFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/home/u/.gemini/tmp/hash/chats/session-2026.json", true},
		{"/home/u/.gemini/tmp/hash/chats/log.jsonl", true},
		{"/home/u/.gemini/tmp/hash/chats/ignore.txt", false},
		{"/home/u/.gemini/tmp/hash/logs.json", true},
	}
	for _, tc := range cases {
		if got := MatchFile(tc.path); got != tc.want {
			t.Errorf("MatchFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
