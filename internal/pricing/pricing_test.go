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
		"gemini-2.5-pro",
		"gemini-2.5-flash",
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

func TestDeepSeekFlashReportedNames(t *testing.T) {
	tb := &Table{prices: map[string]Price{
		"deepseek-flash":               {Input: 3e-7, Output: 1.2e-6},
		"azure_ai/deepseek-v4.1-flash": {Input: 9e-7},
	}, cache: map[string]*Price{}}
	for _, name := range []string{"DeepSeek-V4.1-Flash", "deepseek-v4.1-flash-1017", "deepseek/deepseek-v4.1-flash"} {
		p, ok := tb.Lookup(name)
		if !ok || p.Output != 1.2e-6 {
			t.Errorf("%s: %+v, %v", name, p, ok)
		}
	}
	p, _ := tb.Lookup("azure_ai/deepseek-v4.1-flash")
	if p.Input != 9e-7 {
		t.Fatal("provider-specific price lost")
	}
	for _, name := range []string{"router/deepseek-v4.1-flash", "deepseek-v4.2-flash", "deepseek-v4.1-flash-unknown"} {
		if _, ok := tb.Lookup(name); ok {
			t.Errorf("invented price for %s", name)
		}
	}
	tb.prices["deepseek-v4.1-flash"] = Price{Input: 2e-7}
	tb.cache = map[string]*Price{}
	p, _ = tb.Lookup("DeepSeek-V4.1-Flash")
	if p.Input != 2e-7 {
		t.Fatal("exact override lost")
	}
}

func TestGeminiProviderPrefixes(t *testing.T) {
	tb := &Table{prices: map[string]Price{
		"gemini/gemini-prefixed":    {Input: 1e-6, Output: 4e-6},
		"vertex_ai/gemini-vertex":   {Input: 2e-6, Output: 8e-6},
		"google/gemini-google-only": {Input: 3e-6, Output: 9e-6},
	}, cache: map[string]*Price{}}
	for model, wantOut := range map[string]float64{
		"gemini-prefixed":    4e-6,
		"gemini-vertex":      8e-6,
		"gemini-google-only": 9e-6,
	} {
		p, ok := tb.Lookup(model)
		if !ok || p.Output != wantOut {
			t.Errorf("Lookup(%q) = %+v ok=%v, want out=%g", model, p, ok, wantOut)
		}
	}
}

func TestFirstPartyPrefixStrippingAndDateSuffix(t *testing.T) {
	tb := &Table{prices: map[string]Price{
		"claude-opus-4-8": {Input: 5e-6, Output: 25e-6},
		"gpt-5":           {Input: 2e-6, Output: 10e-6},
	}, cache: map[string]*Price{}}
	for _, tc := range []struct {
		model   string
		wantOut float64
	}{
		{"anthropic/claude-opus-4-8", 25e-6},
		{"anthropic/claude-opus-4-8-20260730", 25e-6},
		{"openai/gpt-5-2026-08-07", 10e-6},
	} {
		p, ok := tb.Lookup(tc.model)
		if !ok || p.Output != tc.wantOut {
			t.Errorf("Lookup(%q) = %+v ok=%v, want out=%g", tc.model, p, ok, tc.wantOut)
		}
	}
}

func TestCostChargesUnsplitCacheCreationRemainder(t *testing.T) {
	tb := &Table{prices: map[string]Price{
		"claude-test": {Input: 1.0, Output: 2.0, CacheWrite: 1.25, CacheWrite1h: 2.0, CacheRead: 0.1},
	}, cache: map[string]*Price{}}
	// Total cacheCreation=100, with 20 explicitly in 1h and 30 in 5m -> 50 unsplit remainder charged at CacheWrite (1.25).
	got, ok := tb.Cost("claude-test", time.Now(), 0, 0, 0, 100, 20, 30)
	if !ok {
		t.Fatal("expected model to price")
	}
	want := 20*2.0 + 30*1.25 + 50*1.25
	if got != want {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
}

func TestLookupFallsBackAcrossReasoningEffortSuffixes(t *testing.T) {
	tb := &Table{prices: map[string]Price{
		"claude-opus-4-6":      {Input: 5e-6, Output: 25e-6},
		"gemini/gemini-4-neon": {Input: 1e-6, Output: 4e-6},
		"gemini-4-neon-xhigh":  {Input: 2e-6, Output: 8e-6}, // explicit variant override wins
	}, cache: map[string]*Price{}}

	if p, ok := tb.Lookup("claude-opus-4-6-medium"); !ok || p.Output != 25e-6 {
		t.Fatalf("claude-opus-4-6-medium = %+v ok=%v, want base 25e-6", p, ok)
	}
	if p, ok := tb.Lookup("gemini-4-neon-high"); !ok || p.Output != 4e-6 {
		t.Fatalf("gemini-4-neon-high = %+v ok=%v, want base 4e-6", p, ok)
	}
	if p, ok := tb.Lookup("gemini-4-neon-xhigh"); !ok || p.Output != 8e-6 {
		t.Fatalf("gemini-4-neon-xhigh = %+v ok=%v, want explicit override 8e-6", p, ok)
	}
}
