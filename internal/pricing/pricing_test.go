package pricing

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T) *Table {
	tb, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestLookupNormalization(t *testing.T) {
	tb := mustLoad(t)
	cases := []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"anthropic.claude-opus-4-8",                  // bedrock w/o region+version
		"us.anthropic.claude-sonnet-4-20250514-v1:0", // full bedrock form
		"gpt-5.6-sol",
		"gpt-5.5",
	}
	for _, m := range cases {
		if _, ok := tb.Lookup(m); !ok {
			t.Errorf("no price resolved for %q", m)
		}
	}
	if _, ok := tb.Lookup("definitely-not-a-model-xyz"); ok {
		t.Error("bogus model should miss")
	}
}

func TestCodexFallbackByDate(t *testing.T) {
	tb := mustLoad(t)
	ts := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	if got := tb.Resolve("codex-auto-review", ts); got != "gpt-5.5" {
		t.Errorf("2026-07 fallback = %q, want gpt-5.5", got)
	}
	if got := tb.Resolve("", time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)); got != "gpt-5-codex" {
		t.Errorf("2025-10 fallback = %q, want gpt-5-codex", got)
	}
	if got := tb.Resolve("gpt-5.5", ts); got != "gpt-5.5" {
		t.Errorf("real model must pass through, got %q", got)
	}
}

func TestCostMath(t *testing.T) {
	tb := &Table{
		prices: map[string]Price{
			"fake-model": {Input: 1e-6, Output: 10e-6, CacheRead: 0.5e-6, CacheWrite: 2e-6, CacheWrite1h: 4e-6},
			"no-cache":   {Input: 1e-6, Output: 10e-6},
		},
		cache: map[string]*Price{},
	}
	now := time.Now()
	// 100 in + 50 out + 200 cache-read + (30×1h + 20×5m) cache-write
	cost, ok := tb.Cost("fake-model", now, 100, 50, 200, 50, 30, 20)
	if !ok {
		t.Fatal("expected pricing")
	}
	want := 100*1e-6 + 50*10e-6 + 200*0.5e-6 + 30*4e-6 + 20*2e-6
	if diff := cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %g, want %g", cost, want)
	}
	// cache read falls back to input rate when unpriced (ccusage rule)
	cost, _ = tb.Cost("no-cache", now, 0, 0, 100, 0, 0, 0)
	if diff := cost - 100*1e-6; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cache-read fallback cost = %g", cost)
	}
	if _, ok := tb.Cost("missing", now, 1, 1, 1, 1, 0, 0); ok {
		t.Error("missing model must return ok=false, never silent zero")
	}
}

func TestOverrides(t *testing.T) {
	tb, err := Load(map[string]Override{
		"my-relay-model": {InputPerM: 2, OutputPerM: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := tb.Lookup("my-relay-model")
	if !ok || p.Input != 2e-6 || p.Output != 8e-6 {
		t.Errorf("override not applied: %+v ok=%v", p, ok)
	}
}
