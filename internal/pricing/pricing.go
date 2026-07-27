// Package pricing computes USD cost from token counts (ADR-0005).
//
// Data source is a build-time trimmed copy of LiteLLM's
// model_prices_and_context_window.json — the same source ccusage uses, so
// numbers are reconcilable. Costs are always computed at query time, never
// stored: facts (tokens) and valuation (prices) stay separate.
package pricing

import (
	"encoding/json"
	_ "embed"
	"regexp"
	"sync"
	"strings"
	"time"
)

//go:embed litellm_prices.json
var rawPrices []byte

// Price holds per-token USD rates (LiteLLM field names).
type Price struct {
	Input        float64 `json:"input_cost_per_token"`
	Output       float64 `json:"output_cost_per_token"`
	CacheRead    float64 `json:"cache_read_input_token_cost"`
	CacheWrite   float64 `json:"cache_creation_input_token_cost"`
	CacheWrite1h float64 `json:"cache_creation_input_token_cost_above_1hr"`
}

// Override is a human-friendly per-1M-token price override from config.
type Override struct {
	InputPerM      float64 `json:"input_per_mtok"`
	OutputPerM     float64 `json:"output_per_mtok"`
	CacheReadPerM  float64 `json:"cache_read_per_mtok"`
	CacheWritePerM float64 `json:"cache_write_per_mtok"`
}

// Table is safe for concurrent use: Lookup memoises normalised model names,
// and HTTP handlers hit it from many goroutines at once (an unguarded map
// write here crashes the process with "concurrent map writes").
type Table struct {
	mu     sync.RWMutex
	prices map[string]Price
	cache  map[string]*Price // normalized lookup memo; nil entry = known miss
}

func Load(overrides map[string]Override) (*Table, error) {
	t := &Table{prices: map[string]Price{}, cache: map[string]*Price{}}
	if err := json.Unmarshal(rawPrices, &t.prices); err != nil {
		return nil, err
	}
	for m, o := range overrides {
		t.prices[strings.ToLower(m)] = Price{
			Input:      o.InputPerM / 1e6,
			Output:     o.OutputPerM / 1e6,
			CacheRead:  o.CacheReadPerM / 1e6,
			CacheWrite: o.CacheWritePerM / 1e6,
		}
	}
	return t, nil
}

// codexFallbacks maps date ranges to the real model behind Codex's synthetic
// model names (codex-auto-review, empty). Mirrors ccusage's
// codex-auto-review-fallbacks.json; newest first.
var codexFallbacks = []struct {
	releasedOn string // YYYY-MM-DD
	model      string
}{
	{"2026-04-23", "gpt-5.5"},
	{"2026-03-05", "gpt-5.4"},
	{"2026-02-05", "gpt-5.3-codex"},
	{"2025-12-11", "gpt-5.2-codex"},
	{"2025-11-13", "gpt-5.1-codex"},
	{"2025-09-15", "gpt-5-codex"},
	{"2025-08-07", "gpt-5"},
}

var bedrockRegion = regexp.MustCompile(`^(us|eu|apac|jp|au|ca|sa|global)\.`)
var bedrockVersion = regexp.MustCompile(`-v\d+(:\d+)?$`)

// Lookup resolves a model ID to a price via the normalization chain:
// exact → provider-prefixed → Bedrock region/version stripping → Vertex form.
func (t *Table) Lookup(model string) (Price, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return Price{}, false
	}
	t.mu.RLock()
	p, cached := t.cache[m]
	t.mu.RUnlock()
	if cached {
		if p == nil {
			return Price{}, false
		}
		return *p, true
	}
	found, ok := t.lookupUncached(m)
	t.mu.Lock()
	if ok {
		t.cache[m] = &found
	} else {
		t.cache[m] = nil
	}
	t.mu.Unlock()
	return found, ok
}

func (t *Table) lookupUncached(m string) (Price, bool) {
	candidates := []string{m, "anthropic/" + m, "openai/" + m}
	// Bedrock: us.anthropic.claude-x-v1:0 → anthropic.claude-x-v1:0 → claude-x
	stripped := bedrockRegion.ReplaceAllString(m, "")
	if stripped != m {
		candidates = append(candidates, stripped)
	}
	bare := bedrockVersion.ReplaceAllString(strings.TrimPrefix(stripped, "anthropic."), "")
	if bare != m {
		candidates = append(candidates, bare)
	}
	// Vertex: claude-x@20250514
	if strings.Contains(m, "@") {
		candidates = append(candidates, "vertex_ai/"+m, strings.ReplaceAll(m, "@", "-"))
	}
	for _, c := range candidates {
		if p, ok := t.prices[c]; ok {
			return p, true
		}
	}
	return Price{}, false
}

// Resolve maps synthetic/unpriced Codex models to the real model of that
// date before lookup. Non-synthetic models pass through unchanged.
func (t *Table) Resolve(model string, ts time.Time) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m != "" && m != "codex-auto-review" {
		return model
	}
	day := ts.UTC().Format("2006-01-02")
	for _, fb := range codexFallbacks {
		if day >= fb.releasedOn {
			return fb.model
		}
	}
	return model
}

// Cost computes USD for one usage aggregate. ccusage-aligned rules:
// cache reads fall back to the input rate when the model has no explicit
// cache-read price; cache writes use the 1h rate for the 1h-TTL share when
// the model prices it separately. Returns ok=false when the model has no
// pricing at all (callers must surface this, not silently count zero).
func (t *Table) Cost(model string, ts time.Time, in, out, cacheRead, cacheCreation, cache1h, cache5m int64) (float64, bool) {
	p, ok := t.Lookup(t.Resolve(model, ts))
	if !ok {
		return 0, false
	}
	crRate := p.CacheRead
	if crRate == 0 {
		crRate = p.Input
	}
	cost := float64(in)*p.Input + float64(out)*p.Output + float64(cacheRead)*crRate
	if p.CacheWrite1h > 0 && cache1h+cache5m > 0 {
		cost += float64(cache1h)*p.CacheWrite1h + float64(cache5m)*p.CacheWrite
	} else {
		cost += float64(cacheCreation) * p.CacheWrite
	}
	return cost, true
}
