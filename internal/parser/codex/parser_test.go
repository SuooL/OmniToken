package codex

import (
	"strings"
	"testing"
)

const sessionMeta = `{"timestamp":"2026-07-26T02:59:50.563Z","type":"session_meta","payload":{"session_id":"s-1","id":"r-1","cwd":"/home/u/proj","cli_version":"0.146.0","model_provider":"openai"}}`
const turnCtx = `{"timestamp":"2026-07-26T02:59:54.006Z","type":"turn_context","payload":{"turn_id":"t-1","cwd":"/home/u/proj","model":"gpt-5.6-sol","effort":"low"}}`

func tokenCount(ts string, last, total string) string {
	s := `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{`
	if last != "" {
		s += `"last_token_usage":` + last + `,`
	}
	s += `"total_token_usage":` + total + `}}}`
	return s
}

func TestParseBasic(t *testing.T) {
	lines := strings.Join([]string{
		sessionMeta,
		turnCtx,
		tokenCount("2026-07-26T03:00:05.966Z",
			`{"input_tokens":22849,"cached_input_tokens":22016,"cache_write_input_tokens":0,"output_tokens":65,"reasoning_output_tokens":47,"total_tokens":22914}`,
			`{"input_tokens":44960,"cached_input_tokens":25600,"cache_write_input_tokens":0,"output_tokens":133,"reasoning_output_tokens":97,"total_tokens":45093}`),
	}, "\n") + "\n"

	events := Parse(strings.NewReader(lines), "dev1").Events
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Model != "gpt-5.6-sol" || e.Source != "codex" || e.Provider != "openai" {
		t.Errorf("attribution: %+v", e)
	}
	if e.InputTokens != 22849-22016 || e.CacheReadTokens != 22016 || e.OutputTokens != 65 {
		t.Errorf("usage split wrong: in=%d cr=%d out=%d", e.InputTokens, e.CacheReadTokens, e.OutputTokens)
	}
	if e.CWD != "/home/u/proj" || e.SessionID != "s-1" {
		t.Errorf("context: %+v", e)
	}
}

func TestRepeatedTokenCountSkipped(t *testing.T) {
	last := `{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":150}`
	total := `{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":150}`
	lines := strings.Join([]string{
		sessionMeta, turnCtx,
		tokenCount("2026-07-26T03:00:05Z", last, total),
		tokenCount("2026-07-26T03:00:09Z", last, total), // stale repeat: totals unchanged
	}, "\n") + "\n"
	events := Parse(strings.NewReader(lines), "d").Events
	if len(events) != 1 {
		t.Fatalf("stale repeat must be skipped: got %d events", len(events))
	}
}

func TestTotalsDeltaFallback(t *testing.T) {
	lines := strings.Join([]string{
		sessionMeta, turnCtx,
		tokenCount("2026-07-26T03:00:05Z", "", `{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":150}`),
		tokenCount("2026-07-26T03:00:09Z", "", `{"input_tokens":300,"cached_input_tokens":60,"cache_write_input_tokens":0,"output_tokens":125,"reasoning_output_tokens":5,"total_tokens":425}`),
	}, "\n") + "\n"
	events := Parse(strings.NewReader(lines), "d").Events
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	// second event = totals delta
	if events[1].InputTokens != (300-100)-(60-10) || events[1].CacheReadTokens != 50 || events[1].OutputTokens != 75 {
		t.Errorf("delta wrong: %+v", events[1])
	}
}

func TestQuotaSnapshots(t *testing.T) {
	rl := `"rate_limits":{"limit_id":"codex","primary":{"used_percent":5.0,"window_minutes":10080,"resets_at":1785635302},"secondary":{"used_percent":38.0,"window_minutes":300,"resets_at":1783736644},"plan_type":"plus"}`
	line := `{"timestamp":"2026-07-26T03:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150},"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}},` + rl + `}}`
	res := Parse(strings.NewReader(sessionMeta+"\n"+turnCtx+"\n"+line+"\n"), "dev1")
	if len(res.Quotas) != 2 {
		t.Fatalf("want 2 quota snapshots (primary+secondary), got %d", len(res.Quotas))
	}
	p := res.Quotas[0]
	if p.Scope != "primary" || p.WindowMinutes != 10080 || p.UsedPercent != 5.0 || p.PlanType != "plus" {
		t.Errorf("primary wrong: %+v", p)
	}
	if p.ResetsAt != 1785635302*1000 {
		t.Errorf("resets_at must be ms: %d", p.ResetsAt)
	}
	if s := res.Quotas[1]; s.Scope != "secondary" || s.WindowMinutes != 300 {
		t.Errorf("secondary wrong: %+v", s)
	}
	if len(res.Events) != 1 {
		t.Errorf("usage event must still be parsed alongside quota: %d", len(res.Events))
	}
}
