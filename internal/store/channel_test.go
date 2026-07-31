package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func channelEvent(id, provider string) model.Event {
	return model.Event{
		EventID: id, TS: time.Now().UnixMilli(), Device: "mac",
		Source: "claude-code", Model: "claude-opus-4-8", Provider: provider,
		InputTokens: 100, OutputTokens: 250, CacheReadTokens: 40, CacheCreationTokens: 10,
	}
}

func providerOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var p string
	if err := s.db.QueryRow(`SELECT provider FROM events WHERE event_id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// ADR-0018 §5 adds the third sanctioned overwrite of an already-stored row.
// It moves the provider column and nothing else, and it only ever moves it
// toward stronger evidence — which is what makes a rescan idempotent.
func TestProviderReclassification(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		rescan string
		want   string
		why    string
	}{
		{
			name: "a disproved model-name fingerprint yields to event-level evidence",
			// The 4,645 events this machine had filed under `bedrock` on the
			// strength of an `anthropic.claude-*` model id, with no Bedrock
			// configured anywhere.
			stored: model.ProviderBedrock, rescan: model.ProviderRelay, want: model.ProviderRelay,
			why: "the model id was never evidence of an endpoint",
		},
		{
			name:   "an unrefined first-party row takes the probe's answer",
			stored: model.ProviderAnthropic, rescan: model.ProviderAnthropicOAuth, want: model.ProviderAnthropicOAuth,
			why: "the probe establishes what the log cannot",
		},
		{
			name: "event-level relay corrects a wrongly refined subscription row",
			// The machine-level probe had painted these rows `anthropic-oauth`
			// because the old rule called every bare `claude-*` id first-party.
			stored: model.ProviderAnthropicOAuth, rescan: model.ProviderRelay, want: model.ProviderRelay,
			why: "the log line saw the endpoint; the probe only saw the machine",
		},
		{
			name:   "today's probe never rewrites yesterday's billing",
			stored: model.ProviderAnthropicOAuth, rescan: model.ProviderAnthropicAPI, want: model.ProviderAnthropicOAuth,
			why: "same evidence strength; the user may have switched plans since",
		},
		{
			name:   "a first-party row is not demoted to the undetermined label",
			stored: model.ProviderAnthropicOAuth, rescan: model.ProviderAnthropic, want: model.ProviderAnthropicOAuth,
			why: "a silent probe is not a reason to discard a past conclusion",
		},
		{
			name:   "a relay verdict survives a later silent scan",
			stored: model.ProviderRelay, rescan: model.ProviderUnknown, want: model.ProviderRelay,
			why: "unknown is weaker than an established endpoint verdict",
		},
		{
			name:   "no evidence still clears a disproved fingerprint",
			stored: model.ProviderVertex, rescan: model.ProviderUnknown, want: model.ProviderUnknown,
			why: "ADR-0018 §6: no evidence is not a licence to keep a bad guess",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			const id = "cc:msg_01RECLASS:req_011Creclass"
			now := time.Now().UnixMilli()
			if _, err := s.InsertEvents([]model.Event{channelEvent(id, tc.stored)}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.InsertEvents([]model.Event{channelEvent(id, tc.rescan)}, now); err != nil {
				t.Fatal(err)
			}
			if got := providerOf(t, s, id); got != tc.want {
				t.Errorf("provider = %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
}

// The hard requirement of ADR-0018 §5.1: attribution moves, totals do not.
func TestProviderReclassificationTouchesNoCountColumn(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UnixMilli()
	ids := []string{"cc:a:1", "cc:b:2", "cc:c:3"}
	stored := []string{model.ProviderBedrock, model.ProviderAnthropic, model.ProviderAnthropicOAuth}
	var batch []model.Event
	for i, id := range ids {
		batch = append(batch, channelEvent(id, stored[i]))
	}
	if _, err := s.InsertEvents(batch, now); err != nil {
		t.Fatal(err)
	}

	type totals struct{ events, in, out, cr, cc int64 }
	read := func() totals {
		var tt totals
		err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0) FROM events`).
			Scan(&tt.events, &tt.in, &tt.out, &tt.cr, &tt.cc)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}
	before := read()

	// Reclassify every row, twice, with counts that would be wrong if the
	// reclassification ever fell through to an insert or an update of a sum.
	for pass := 0; pass < 2; pass++ {
		var again []model.Event
		for _, id := range ids {
			e := channelEvent(id, model.ProviderRelay)
			e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens = 999, 999, 999, 999
			again = append(again, e)
		}
		if _, err := s.InsertEvents(again, now); err != nil {
			t.Fatal(err)
		}
	}
	if after := read(); after != before {
		t.Errorf("counts moved: got %+v, want %+v", after, before)
	}
	// bedrock and anthropic yield to the relay verdict; the probe-confirmed row
	// yields too, because event-level evidence outranks the machine-level probe.
	for _, id := range ids {
		if got := providerOf(t, s, id); got != model.ProviderRelay {
			t.Errorf("%s: provider = %q, want relay", id, got)
		}
	}
}

// §5.3: replaying the same evidence any number of times settles on one value,
// and the order the evidence arrives in does not change where it settles.
func TestProviderReclassificationIsOrderIndependent(t *testing.T) {
	sequences := [][]string{
		{model.ProviderBedrock, model.ProviderAnthropic, model.ProviderAnthropicOAuth, model.ProviderRelay},
		{model.ProviderRelay, model.ProviderAnthropicOAuth, model.ProviderAnthropic, model.ProviderBedrock},
		{model.ProviderAnthropicOAuth, model.ProviderBedrock, model.ProviderRelay, model.ProviderAnthropic},
	}
	for i, seq := range sequences {
		s := openTestStore(t)
		const id = "cc:order:1"
		now := time.Now().UnixMilli()
		for _, p := range seq {
			if _, err := s.InsertEvents([]model.Event{channelEvent(id, p)}, now); err != nil {
				t.Fatal(err)
			}
		}
		if got := providerOf(t, s, id); got != model.ProviderRelay {
			t.Errorf("sequence %d settled on %q, want relay (the strongest evidence in the set)", i, got)
		}
	}
}

// §5.1 again, from the other side: the reclassification must not disturb the
// two overwrites that were already sanctioned.
func TestProviderReclassificationLeavesSourceAndDeviceAlone(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UnixMilli()
	const id = "cc:cols:1"
	first := channelEvent(id, model.ProviderBedrock)
	first.Device, first.Source = "mbp", "proxy"
	if _, err := s.InsertEventsFrom([]model.Event{first}, now, OriginObserved); err != nil {
		t.Fatal(err)
	}
	second := channelEvent(id, model.ProviderRelay)
	second.Device, second.Source = "mbp", "proxy" // no source/device news
	if _, err := s.InsertEventsFrom([]model.Event{second}, now, OriginObserved); err != nil {
		t.Fatal(err)
	}
	var device, source, origin string
	err := s.db.QueryRow(`SELECT device, source, device_origin FROM events WHERE event_id = ?`, id).
		Scan(&device, &source, &origin)
	if err != nil {
		t.Fatal(err)
	}
	if device != "mbp" || source != "proxy" || origin != "observed" {
		t.Errorf("reclassification disturbed other columns: device=%q source=%q origin=%q", device, source, origin)
	}
	if got := providerOf(t, s, id); got != model.ProviderRelay {
		t.Errorf("provider = %q, want relay", got)
	}
}

// A database written before ADR-0018 carries provider labels produced by the
// disproved model-name rule. Bedrock and Vertex were only ever reachable that
// way, so on open they are demoted to unknown: a rescan can then restore real
// evidence for whatever logs survive, and rows whose logs are gone show up in
// the panel's unknown column instead of inflating "official API" (§6).
func TestLegacyFingerprintMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s := openLegacy(t, path)
	now := time.Now().UnixMilli()
	seed := []model.Event{
		channelEvent("cc:leg-bedrock", model.ProviderBedrock),
		channelEvent("cc:leg-vertex", model.ProviderVertex),
		channelEvent("cc:leg-oauth", model.ProviderAnthropicOAuth),
		channelEvent("cc:leg-anthropic", model.ProviderAnthropic),
		channelEvent("cc:leg-relay", model.ProviderRelay),
	}
	if _, err := s.InsertEvents(seed, now); err != nil {
		t.Fatal(err)
	}
	var tokensBefore, countBefore int64
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens+output_tokens),0) FROM events`).
		Scan(&countBefore, &tokensBefore); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	want := map[string]string{
		"cc:leg-bedrock":   model.ProviderUnknown,
		"cc:leg-vertex":    model.ProviderUnknown,
		"cc:leg-oauth":     model.ProviderAnthropicOAuth,
		"cc:leg-anthropic": model.ProviderAnthropic,
		"cc:leg-relay":     model.ProviderRelay,
	}
	for id, wantProvider := range want {
		if got := providerOf(t, reopened, id); got != wantProvider {
			t.Errorf("%s: provider = %q, want %q", id, got, wantProvider)
		}
	}
	var tokensAfter, countAfter int64
	if err := reopened.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens+output_tokens),0) FROM events`).
		Scan(&countAfter, &tokensAfter); err != nil {
		t.Fatal(err)
	}
	if countAfter != countBefore || tokensAfter != tokensBefore {
		t.Errorf("migration moved counts: %d/%d, want %d/%d", countAfter, tokensAfter, countBefore, tokensBefore)
	}

	// Re-opening must not undo a reclassification that has since happened.
	if _, err := reopened.InsertEvents([]model.Event{channelEvent("cc:leg-bedrock", model.ProviderRelay)}, now); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	third, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if got := providerOf(t, third, "cc:leg-bedrock"); got != model.ProviderRelay {
		t.Errorf("re-open wiped a reclassified row: %q", got)
	}
}

// openLegacy builds a store whose events table predates the migration flag, so
// Open() has to run the demotion.
func openLegacy(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM app_settings WHERE key = ?`, providerReclassKey); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChannelUsageSplitsAndKeepsTheTotalWhole(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	ms := now.UnixMilli()
	seed := []struct {
		id, source, provider string
		tokens               int64
	}{
		{"a", "claude-code", model.ProviderAnthropicOAuth, 100},
		{"b", "claude-code", model.ProviderRelay, 40},
		{"c", "claude-code", model.ProviderBedrock, 10},
		{"d", "claude-code", model.ProviderAnthropic, 7},
		{"e", "codex", model.ProviderOpenAIChatGPT, 200},
		{"f", "codex", "custom", 60},
		{"g", "codex", model.ProviderUnknown, 3},
	}
	var batch []model.Event
	for _, row := range seed {
		batch = append(batch, model.Event{
			EventID: row.id, TS: ms, Device: "mac", Source: row.source,
			Model: "m", Provider: row.provider, InputTokens: row.tokens,
		})
	}
	if _, err := s.InsertEvents(batch, ms); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ChannelBreakdown(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	var events int64
	for _, r := range rows {
		got[r.Channel] = r.TotalTokens
		events += r.Events
	}
	want := map[string]int64{
		model.ChannelSubscription: 300, // 100 + 200
		model.ChannelAPI:          10,  // legacy bedrock label
		model.ChannelRelay:        100, // 40 + 60
		model.ChannelUnknown:      10,  // 7 + 3, never folded into another column
	}
	for channel, wantTokens := range want {
		if got[channel] != wantTokens {
			t.Errorf("%s = %d tokens, want %d", channel, got[channel], wantTokens)
		}
	}
	if events != int64(len(seed)) {
		t.Errorf("channels account for %d events, want %d — the split must be a partition", events, len(seed))
	}
	// Every channel is reported even at zero, so the panel can show a share
	// rather than silently omitting a column (ADR-0018 §6).
	if len(rows) != len(model.Channels()) {
		t.Errorf("got %d channel rows, want %d", len(rows), len(model.Channels()))
	}
}

// ADR-0018 §7: a quota window only constrains subscription usage. Relay traffic
// in the same 5h block is what made the panel's numbers contradict each other.
func TestBlocksCountSubscriptionOnly(t *testing.T) {
	s := openTestStore(t)
	ms := time.Now().UnixMilli()
	batch := []model.Event{
		{EventID: "s1", TS: ms, Source: "claude-code", Provider: model.ProviderAnthropicOAuth, InputTokens: 100},
		{EventID: "r1", TS: ms, Source: "claude-code", Provider: model.ProviderRelay, InputTokens: 1000},
		{EventID: "u1", TS: ms, Source: "claude-code", Provider: model.ProviderAnthropic, InputTokens: 500},
		{EventID: "a1", TS: ms, Source: "claude-code", Provider: model.ProviderAnthropicAPI, InputTokens: 300},
	}
	if _, err := s.InsertEvents(batch, ms); err != nil {
		t.Fatal(err)
	}
	blocks, err := s.Blocks(time.UnixMilli(ms-time.Hour.Milliseconds()), time.UnixMilli(ms))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, b := range blocks {
		total += b.Tokens
	}
	if total != 100 {
		t.Errorf("block tokens = %d, want 100 (only the subscription event)", total)
	}
}

func TestIsSubscriptionFollowsTheChannelMapping(t *testing.T) {
	yes := []string{model.ProviderAnthropicOAuth, model.ProviderOpenAIChatGPT}
	no := []string{
		model.ProviderAnthropic, model.ProviderAnthropicAPI, model.ProviderRelay,
		model.ProviderUnknown, model.ProviderOpenAI, model.ProviderBedrock, "custom", "",
	}
	for _, p := range yes {
		if !IsSubscription("claude-code", p) {
			t.Errorf("IsSubscription(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsSubscription("claude-code", p) {
			t.Errorf("IsSubscription(%q) = true, want false", p)
		}
	}
}

// The overwrite guard mirrors model.ProviderRank in SQL. If the two ever
// disagree a reclassification could silently demote a row, so pin them together
// over the whole value domain, relay names included.
func TestProviderRankSQLMatchesModel(t *testing.T) {
	s := openTestStore(t)
	labels := append(model.RankedProviders(),
		"custom", "sub2api", "enjoy", "aihub", "trellisreview", "OpenAI", "myrelay")
	ms := time.Now().UnixMilli()
	for i, p := range labels {
		id := "rank:" + p + string(rune('a'+i))
		if _, err := s.InsertEvents([]model.Event{{EventID: id, TS: ms, Provider: p}}, ms); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := s.db.QueryRow(`SELECT `+providerRankSQL+` FROM events WHERE event_id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := model.ProviderRank(p); got != want {
			t.Errorf("SQL rank of %q = %d, Go says %d", p, got, want)
		}
	}
}
