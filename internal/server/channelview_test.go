package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

func channelTestServer(t *testing.T, events []model.Event) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "chan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if len(events) > 0 {
		if _, err := st.InsertEvents(events, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &Config{Listen: "127.0.0.1:0"}, store: st}
	s.setPrices(prices)
	return s
}

func channelSeed(now time.Time) []model.Event {
	ms := now.UnixMilli()
	mk := func(id, source, provider string, tokens int64) model.Event {
		return model.Event{
			EventID: id, TS: ms, Device: "mac", Source: source,
			Model: "claude-opus-4-8", Provider: provider, InputTokens: tokens,
		}
	}
	return []model.Event{
		mk("sub-cc", "claude-code", model.ProviderAnthropicOAuth, 100),
		mk("sub-cx", "codex", model.ProviderOpenAIChatGPT, 200),
		mk("api-1", "claude-code", model.ProviderAnthropicAPI, 30),
		mk("relay-cc", "claude-code", model.ProviderRelay, 40),
		mk("relay-cx", "codex", "custom", 60),
		mk("unk-1", "claude-code", model.ProviderAnthropic, 7),
	}
}

// The panel needs the four channels as separate, addable numbers — that is the
// whole request behind ADR-0018.
func TestOverviewExposesChannelBreakdown(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	rec := httptest.NewRecorder()
	s.handleOverview(rec, httptest.NewRequest("GET", "/api/v1/overview?days=30", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ByChannel []struct {
			Channel     string `json:"channel"`
			Label       string `json:"label"`
			TotalTokens int64  `json:"total_tokens"`
			Events      int64  `json:"events"`
		} `json:"by_channel"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ByChannel) != 4 {
		t.Fatalf("want 4 channel rows, got %d", len(resp.ByChannel))
	}
	want := map[string]int64{
		model.ChannelSubscription: 300,
		model.ChannelAPI:          30,
		model.ChannelRelay:        100,
		model.ChannelUnknown:      7,
	}
	var events int64
	for _, row := range resp.ByChannel {
		if row.Label == "" {
			t.Errorf("channel %q has no label", row.Channel)
		}
		if row.TotalTokens != want[row.Channel] {
			t.Errorf("%s = %d tokens, want %d", row.Channel, row.TotalTokens, want[row.Channel])
		}
		events += row.Events
	}
	if events != 6 {
		t.Errorf("channel rows cover %d events, want 6 — the split must be a partition", events)
	}
}

// ADR-0018 §7: pay-per-use and relay traffic get their own rolling cards. They
// used to share one "API 计费" card with everything that was not subscription,
// which is how relay spend ended up presented as official API spend.
func TestWindowCardsSplitMeteredChannels(t *testing.T) {
	now := time.Now()
	s := channelTestServer(t, channelSeed(now))
	cards, err := s.buildWindowCards(now, nil)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]windowCard{}
	for _, c := range cards {
		byKey[c.Key] = c
	}
	for key, wantTokens := range map[string]int64{
		"claude-code": 100, "codex": 200,
		model.ChannelAPI: 30, model.ChannelRelay: 100, model.ChannelUnknown: 7,
	} {
		card, ok := byKey[key]
		if !ok {
			t.Errorf("no card for %q", key)
			continue
		}
		if card.Tokens != wantTokens {
			t.Errorf("card %q = %d tokens, want %d", key, card.Tokens, wantTokens)
		}
	}
	// A quota bar on a metered channel would be a category error (ADR-0018 §7).
	for _, key := range []string{model.ChannelAPI, model.ChannelRelay, model.ChannelUnknown} {
		if c := byKey[key]; c.UsedPercent != 0 || c.ResetsAt != 0 || c.Kind == "subscription" {
			t.Errorf("card %q carries subscription-window fields: %+v", key, c)
		}
	}
	// The subscription cards must exclude everything else on the same source.
	if c := byKey["claude-code"]; c.Tokens != 100 {
		t.Errorf("claude-code subscription card = %d, want 100 (relay/api/unknown excluded)", c.Tokens)
	}
}

// An empty metered channel produces no card, so the live page does not grow
// three permanent zero rows.
func TestWindowCardsOmitEmptyMeteredChannels(t *testing.T) {
	now := time.Now()
	ms := now.UnixMilli()
	s := channelTestServer(t, []model.Event{
		{EventID: "only-sub", TS: ms, Source: "claude-code", Model: "m",
			Provider: model.ProviderAnthropicOAuth, InputTokens: 10},
	})
	cards, err := s.buildWindowCards(now, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.Kind != "subscription" {
			t.Errorf("unexpected metered card with no usage: %+v", c)
		}
	}
}

func TestPeriodCostKeepsUnknownSeparate(t *testing.T) {
	s := channelTestServer(t, nil)
	rows := []store.ModelUsageRow{
		{Model: "claude-opus-4-8", Provider: model.ProviderAnthropicOAuth, Totals: store.Totals{InputTokens: 1_000_000}},
		{Model: "claude-opus-4-8", Provider: model.ProviderRelay, Totals: store.Totals{InputTokens: 1_000_000}},
		{Model: "claude-opus-4-8", Provider: model.ProviderAnthropic, Totals: store.Totals{InputTokens: 1_000_000}},
	}
	pc, _, _ := s.costFromUsage(rows)
	if pc.EquivalentUSD <= 0 || pc.RealUSD <= 0 || pc.UnknownUSD <= 0 {
		t.Fatalf("want all three buckets populated, got %+v", pc)
	}
	if pc.EquivalentUSD != pc.RealUSD || pc.RealUSD != pc.UnknownUSD {
		t.Errorf("identical usage priced differently across buckets: %+v", pc)
	}
}
