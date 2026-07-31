package codex

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/suool/omnitoken/internal/model"
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

	events := Parse(strings.NewReader(lines), "dev1", 0).Events
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
	events := Parse(strings.NewReader(lines), "d", 0).Events
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
	events := Parse(strings.NewReader(lines), "d", 0).Events
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	// second event = totals delta
	if events[1].InputTokens != (300-100)-(60-10) || events[1].CacheReadTokens != 50 || events[1].OutputTokens != 75 {
		t.Errorf("delta wrong: %+v", events[1])
	}
}

// --- turn bracketing (ADR-0009, 2026-07-31 revision) ---
//
// Shapes copied from real rollouts under ~/.codex/sessions: task_started and
// task_complete bracket a turn by FILE POSITION, the token_count lines in
// between carry no turn_id, and the closing line's started_at/completed_at/
// duration_ms/time_to_first_token_ms are Codex's own authoritative timing
// rather than anything derived from the log line's timestamp.

func taskStarted(ts string, startedAt int64) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"task_started",` +
		`"turn_id":"019fb32d-ff2e-7f53-8849-b3afd49b81d5","started_at":` + itoa(startedAt) +
		`,"model_context_window":258400,"collaboration_mode_kind":"default"}}`
}

func taskComplete(ts string, startedAt, completedAt, durationMS, ttftMS int64) string {
	s := `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"task_complete",` +
		`"turn_id":"019fb32d-ff2e-7f53-8849-b3afd49b81d5","last_agent_message":"done",` +
		`"started_at":` + itoa(startedAt) + `,"completed_at":` + itoa(completedAt) +
		`,"duration_ms":` + itoa(durationMS)
	if ttftMS > 0 {
		s += `,"time_to_first_token_ms":` + itoa(ttftMS)
	}
	return s + `}}`
}

func turnAborted(ts string, startedAt, completedAt, durationMS int64) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"turn_aborted",` +
		`"turn_id":"019fb5a4-450f-7400-ae9b-a06b8b85c335","reason":"interrupted","started_at":` +
		itoa(startedAt) + `,"completed_at":` + itoa(completedAt) + `,"duration_ms":` + itoa(durationMS) + `}}`
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func usageJSON(out, reasoning int64) string {
	return `{"input_tokens":1000,"cached_input_tokens":0,"cache_write_input_tokens":0,` +
		`"output_tokens":` + itoa(out) + `,"reasoning_output_tokens":` + itoa(reasoning) +
		`,"total_tokens":` + itoa(1000+out+reasoning) + `}`
}

// One real turn: 03:00:00 start, three model responses, closed at 03:00:50.5
// with duration 50,500ms and TTFT 5,000ms — so 45,500ms of generation.
func turnLines() []string {
	const startedAt, completedAt = 1785034800, 1785034850
	return []string{
		sessionMeta, turnCtx,
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		tokenCount("2026-07-26T03:00:12.000Z", usageJSON(100, 0), usageJSON(100, 0)),
		tokenCount("2026-07-26T03:00:30.000Z", usageJSON(150, 50), usageJSON(300, 50)),
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(200, 100), usageJSON(600, 200)),
		taskComplete("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500, 5000),
	}
}

func parseLines(t *testing.T, lines []string) []model.Event {
	t.Helper()
	return Parse(strings.NewReader(strings.Join(lines, "\n")+"\n"), "d", 0).Events
}

// unionMS merges [ts-gen_ms, ts] the way store.SpeedSeries and
// store.LiveSpeedSince do, so the test asserts what those views will measure.
func unionMS(events []model.Event) int64 {
	type span struct{ start, end int64 }
	var spans []span
	for _, e := range events {
		if e.GenMS > 0 {
			spans = append(spans, span{e.TS - e.GenMS, e.TS})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var total, curStart, curEnd int64
	for i, s := range spans {
		if i == 0 || s.start > curEnd {
			total += curEnd - curStart
			curStart, curEnd = s.start, s.end
			continue
		}
		curEnd = max(curEnd, s.end)
	}
	return total + curEnd - curStart
}

func TestTurnGenerationIntervalIsAuthoritative(t *testing.T) {
	events := parseLines(t, turnLines())
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	const gen = 50500 - 5000
	// Every event of a measured turn carries part of the interval, so the union
	// is the turn's generation time and Σoutput is the whole turn's output.
	for i, e := range events {
		if e.GenMS <= 0 {
			t.Errorf("event %d has no generation interval: %+v", i, e)
		}
	}
	if got := unionMS(events); got != gen {
		t.Errorf("union of generation intervals = %d, want %d (duration_ms - time_to_first_token_ms)", got, gen)
	}
	// The interval must end at the last response and start where generation
	// began, not at the turn's own first log line.
	if start := events[0].TS - events[0].GenMS; start != events[2].TS-gen {
		t.Errorf("interval starts at %d, want %d", start, events[2].TS-gen)
	}
	// TTFT is Codex's own exact number: reported once, on the response whose
	// latency it measured.
	if events[0].TTFTMS != 5000 {
		t.Errorf("first event ttft_ms = %d, want 5000", events[0].TTFTMS)
	}
	for i, e := range events[1:] {
		if e.TTFTMS != 0 {
			t.Errorf("event %d must not repeat the turn's TTFT: %d", i+1, e.TTFTMS)
		}
	}
	var out int64
	for _, e := range events {
		out += e.OutputTokens
	}
	if out != 100+150+200 {
		t.Errorf("output tokens over the turn = %d, want 450", out)
	}
}

// A resumed session replays the whole prior thread into a new rollout with the
// flush time on every line, while started_at/completed_at keep the real epoch.
// Those turns must be left unmeasured rather than dropped onto the timeline at
// the moment of the replay — the ADR's "no data" is not "zero speed".
func TestReplayedTurnCarriesNoInterval(t *testing.T) {
	const flush = "2026-07-26T09:13:26.384Z"
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskStarted(flush, startedAt),
		tokenCount(flush, usageJSON(100, 0), usageJSON(100, 0)),
		tokenCount("2026-07-26T09:13:26.385Z", usageJSON(150, 50), usageJSON(300, 50)),
		taskComplete("2026-07-26T09:13:26.386Z", startedAt, completedAt, 50500, 5000),
	}
	events := parseLines(t, lines)
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	for i, e := range events {
		if e.GenMS != 0 || e.TTFTMS != 0 {
			t.Errorf("replayed event %d must stay unmeasured: gen=%d ttft=%d", i, e.GenMS, e.TTFTMS)
		}
	}
}

// turn_aborted and older task_complete records carry no time_to_first_token_ms,
// so the generation interval cannot be separated from the wait for it.
func TestTurnWithoutTTFTCarriesNoInterval(t *testing.T) {
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		tokenCount("2026-07-26T03:00:12.000Z", usageJSON(100, 0), usageJSON(100, 0)),
		turnAborted("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500),
	}
	events := parseLines(t, lines)
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].GenMS != 0 || events[0].TTFTMS != 0 {
		t.Errorf("aborted turn must stay unmeasured: %+v", events[0])
	}
}

// The turn in flight has no closing record yet; the next full reparse (Codex
// files are re-read whole) fills the interval in through the store's UPSERT.
func TestOpenTurnCarriesNoIntervalYet(t *testing.T) {
	lines := turnLines()
	events := parseLines(t, lines[:len(lines)-1])
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	for i, e := range events {
		if e.GenMS != 0 {
			t.Errorf("event %d of an unfinished turn must stay unmeasured: %d", i, e.GenMS)
		}
	}
}

// A turn whose token_count lines span more than the generation interval (tool
// output arriving after the last response) must still union to exactly the
// authoritative interval, and no event may lose its tokens to a zero interval.
func TestOverlongTurnSpanScalesToAuthoritativeInterval(t *testing.T) {
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		tokenCount("2026-07-26T03:00:12.000Z", usageJSON(100, 0), usageJSON(100, 0)),
		tokenCount("2026-07-26T03:00:30.000Z", usageJSON(150, 50), usageJSON(300, 50)),
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(200, 100), usageJSON(600, 200)),
		taskComplete("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500, 40000),
	}
	events := parseLines(t, lines)
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if got := unionMS(events); got != 50500-40000 {
		t.Errorf("union = %d, want %d", got, 50500-40000)
	}
	for i, e := range events {
		if e.GenMS <= 0 {
			t.Errorf("event %d lost its interval and would drop out of Σoutput: %+v", i, e)
		}
	}
}

// A one-response turn takes the whole interval, and the noise around it
// (agent_message, patch_apply_end, a closing record with no turn open) must
// neither create nor move an interval.
func TestSingleResponseTurnAndSurroundingNoise(t *testing.T) {
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskComplete("2026-07-26T02:59:59.000Z", startedAt, completedAt, 1000, 100), // no turn open
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		`{"timestamp":"2026-07-26T03:00:11.000Z","type":"event_msg","payload":{"type":"agent_message","message":"x"}}`,
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(400, 100), usageJSON(400, 100)),
		taskComplete("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500, 5000),
	}
	events := parseLines(t, lines)
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].GenMS != 50500-5000 || events[0].TTFTMS != 5000 {
		t.Errorf("single-response turn: gen=%d ttft=%d", events[0].GenMS, events[0].TTFTMS)
	}
}

// Two responses written in the same millisecond still both need an interval:
// the views that union the intervals sum output_tokens over the same rows, so a
// zero interval would silently drop a response's tokens from the numerator.
func TestNoEventLosesItsIntervalToRounding(t *testing.T) {
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(100, 0), usageJSON(100, 0)),
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(150, 50), usageJSON(300, 50)),
		taskComplete("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500, 5000),
	}
	events := parseLines(t, lines)
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	for i, e := range events {
		if e.GenMS <= 0 {
			t.Errorf("event %d has no interval: %+v", i, e)
		}
	}
	if got := unionMS(events); got != 50500-5000 {
		t.Errorf("union = %d, want %d", got, 50500-5000)
	}
}

// Only part of the turn was replayed: the closing record's clocks agree, but an
// event inside it carries a timestamp from outside the turn's own window.
func TestPartiallyReplayedTurnCarriesNoInterval(t *testing.T) {
	const startedAt, completedAt = 1785034800, 1785034850
	lines := []string{
		sessionMeta, turnCtx,
		taskStarted("2026-07-26T03:00:00.010Z", startedAt),
		tokenCount("2026-07-26T00:12:00.000Z", usageJSON(100, 0), usageJSON(100, 0)), // hours earlier
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(150, 50), usageJSON(300, 50)),
		taskComplete("2026-07-26T03:00:50.500Z", startedAt, completedAt, 50500, 5000),
	}
	events := parseLines(t, lines)
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	for i, e := range events {
		if e.GenMS != 0 || e.TTFTMS != 0 {
			t.Errorf("event %d of a partially replayed turn must stay unmeasured: %+v", i, e)
		}
	}
}

// ADR-0004's iron law: identity may not move. Bracketing turns adds no line to
// the sequence and reads no field the id is built from.
func TestEventIDsUnaffectedByTurnBracketing(t *testing.T) {
	full := parseLines(t, turnLines())
	bare := parseLines(t, []string{
		sessionMeta, turnCtx,
		tokenCount("2026-07-26T03:00:12.000Z", usageJSON(100, 0), usageJSON(100, 0)),
		tokenCount("2026-07-26T03:00:30.000Z", usageJSON(150, 50), usageJSON(300, 50)),
		tokenCount("2026-07-26T03:00:45.000Z", usageJSON(200, 100), usageJSON(600, 200)),
	})
	if len(full) != len(bare) {
		t.Fatalf("event count changed: %d vs %d", len(full), len(bare))
	}
	for i := range full {
		if full[i].EventID != bare[i].EventID {
			t.Errorf("event %d id moved: %s != %s", i, full[i].EventID, bare[i].EventID)
		}
	}
}

// Golden ids: a literal so that any change to the derivation (rollout id,
// timestamp, sequence, usage) fails here instead of silently double-counting
// every event already in the database.
func TestEventIDsStable(t *testing.T) {
	events := parseLines(t, turnLines())
	want := []string{
		"cx:2dcff158c0c1785ceda46ff4",
		"cx:f273419784fdb6ae06447a5d",
		"cx:db1fc090060864cbb8351ff9",
	}
	for i, e := range events {
		if i >= len(want) {
			break
		}
		if e.EventID != want[i] {
			t.Errorf("event %d id = %s, want %s (event_id derivation must never change)", i, e.EventID, want[i])
		}
	}
}

func TestQuotaSnapshots(t *testing.T) {
	rl := `"rate_limits":{"limit_id":"codex","primary":{"used_percent":5.0,"window_minutes":10080,"resets_at":1785635302},"secondary":{"used_percent":38.0,"window_minutes":300,"resets_at":1783736644},"plan_type":"plus"}`
	line := `{"timestamp":"2026-07-26T03:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150},"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}},` + rl + `}}`
	res := Parse(strings.NewReader(sessionMeta+"\n"+turnCtx+"\n"+line+"\n"), "dev1", 0)
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

// Codex billing channel (ADR-0018 §3, criterion corrected against real data).
//
// The ADR proposed "the rollout contains a rate_limits payload" as the
// subscription test. Measured on 610 local rollouts it is worthless: 523 of 523
// sessions that have any token_count line carry rate_limits, for every provider
// alike — accuracy 20.8%, worse than always answering no. What does discriminate
// is what is INSIDE that payload: plan_type and credits are non-null only when
// the real OpenAI account answered. Across 22,547 token_count lines from
// custom/sub2api/enjoy/aihub/trellisreview, both are null every single time.
//
// The second half of the rule is the provider name, matched case-sensitively:
// this machine's config.toml defines a `[model_providers.OpenAI]` pointing at a
// relay, and 137 sessions are branded "OpenAI" while never touching OpenAI.
// Only the lowercase `openai` is Codex's built-in id. Requiring both signals is
// what keeps a relay that forwards someone else's plan_type out of the
// subscription column.
func rateLimited(ts, extra string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count",` +
		`"info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150},` +
		`"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}},` +
		`"rate_limits":{"limit_id":"codex","primary":{"used_percent":5.0,"window_minutes":10080,"resets_at":1785635302}` + extra + `}}}`
}

func parseWithProvider(t *testing.T, provider, tokenLine string) string {
	t.Helper()
	meta := strings.Replace(sessionMeta, `"model_provider":"openai"`, `"model_provider":"`+provider+`"`, 1)
	events := Parse(strings.NewReader(meta+"\n"+turnCtx+"\n"+tokenLine+"\n"), "d", 0).Events
	if len(events) != 1 {
		t.Fatalf("provider %q: want 1 event, got %d", provider, len(events))
	}
	return events[0].Provider
}

func TestCodexPlanEvidenceMarksSubscription(t *testing.T) {
	for _, extra := range []string{
		`,"plan_type":"plus"`,
		`,"plan_type":"team"`,
		`,"credits":{"balance":0,"has_credits":false,"unlimited":false}`,
	} {
		if got := parseWithProvider(t, "openai", rateLimited("2026-07-26T03:00:05Z", extra)); got != "openai-chatgpt" {
			t.Errorf("rate_limits%s: provider = %q, want openai-chatgpt", extra, got)
		}
	}
}

// The bare presence of rate_limits proves nothing — every relay emits it too.
func TestCodexRateLimitsAloneIsNotSubscription(t *testing.T) {
	line := rateLimited("2026-07-26T03:00:05Z", `,"plan_type":null,"credits":null`)
	if got := parseWithProvider(t, "openai", line); got != "openai" {
		t.Errorf("provider = %q, want openai (first-party declared, payment unproven)", got)
	}
	for _, relay := range []string{"custom", "sub2api", "enjoy", "aihub", "trellisreview"} {
		if got := parseWithProvider(t, relay, line); got != relay {
			t.Errorf("%s: provider = %q, want the declared name kept", relay, got)
		}
	}
}

// A relay that names itself "OpenAI" and forwards a shared account's plan_type
// must not land in the subscription column. Casing is the only thing telling it
// apart from Codex's built-in provider id, so the match must not fold case.
func TestCodexProviderNameIsCaseSensitive(t *testing.T) {
	line := rateLimited("2026-07-26T03:00:05Z", `,"plan_type":"plus"`)
	if got := parseWithProvider(t, "OpenAI", line); got != "OpenAI" {
		t.Errorf(`provider = %q, want "OpenAI" kept as a relay name`, got)
	}
}

// Plan evidence usually arrives after the first token_count line, so the
// verdict has to be applied to the whole session once the file is read out.
// Codex rollouts are re-parsed whole on every growth (SourceSpec.FullReparse),
// which is what makes a file-scoped conclusion sound here.
func TestCodexPlanEvidenceAppliesToEarlierEventsInTheSession(t *testing.T) {
	first := tokenCount("2026-07-26T03:00:05Z",
		`{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}`,
		`{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}`)
	second := rateLimited("2026-07-26T03:00:09Z", `,"plan_type":"plus"`)
	// The second line repeats the same totals, so only the first yields an event.
	events := Parse(strings.NewReader(sessionMeta+"\n"+turnCtx+"\n"+first+"\n"+second+"\n"), "d", 0).Events
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Provider != "openai-chatgpt" {
		t.Errorf("provider = %q, want openai-chatgpt applied retroactively", events[0].Provider)
	}
}

func TestCodexMissingProviderIsUnknown(t *testing.T) {
	line := tokenCount("2026-07-26T03:00:05Z", "",
		`{"input_tokens":100,"cached_input_tokens":10,"output_tokens":50,"total_tokens":150}`)
	meta := strings.Replace(sessionMeta, `,"model_provider":"openai"`, "", 1)
	events := Parse(strings.NewReader(meta+"\n"+turnCtx+"\n"+line+"\n"), "d", 0).Events
	if len(events) != 1 || events[0].Provider != "unknown" {
		t.Fatalf("want one unknown-provider event, got %+v", events)
	}
}

// The classification must not move the event id (ADR-0004 / ADR-0018 §5.2).
func TestCodexEventIDIndependentOfProvider(t *testing.T) {
	line := rateLimited("2026-07-26T03:00:05Z", `,"plan_type":"plus"`)
	plain := rateLimited("2026-07-26T03:00:05Z", `,"plan_type":null`)
	withPlan := Parse(strings.NewReader(sessionMeta+"\n"+turnCtx+"\n"+line+"\n"), "d", 0).Events
	without := Parse(strings.NewReader(sessionMeta+"\n"+turnCtx+"\n"+plain+"\n"), "d", 0).Events
	if len(withPlan) != 1 || len(without) != 1 {
		t.Fatalf("want one event each, got %d and %d", len(withPlan), len(without))
	}
	if withPlan[0].Provider == without[0].Provider {
		t.Fatal("test is vacuous: the two runs must classify differently")
	}
	if withPlan[0].EventID != without[0].EventID {
		t.Errorf("event id moved with the classification: %q vs %q", withPlan[0].EventID, without[0].EventID)
	}
}
