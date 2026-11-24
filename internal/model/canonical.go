package model

import (
	"regexp"
	"strings"
)

// Canonical model and source names for display and aggregation.
//
// The same model reaches us through several channels and arrives under
// different ids: `claude-opus-4-8` direct and `anthropic.claude-opus-4-8` via
// Bedrock are one model, and showing them as two rows splits a total that
// should be whole. The channel is not lost — it lives in the event's provider
// field, which is where that question belongs.
//
// A pinned release date is the same story told by the vendor rather than the
// router: `claude-haiku-4-5-20251001` is what the alias `claude-haiku-4-5`
// resolves to, and one database held three rows for that single model once the
// Bedrock prefix was counted too.
//
// Vendor routing and pinned dates are stripped; nothing else is. The family
// name stays — `claude-` and `gpt-` say what something is, and a list of bare
// version numbers would be harder to read, not easier — and so does the model
// number, which a looser date rule would happily eat.
//
// These names are never written to the database. Pricing keys off the id the
// tool actually reported, and rewriting history to match a display preference
// would be a bad trade.
//
// # Where to fold
//
// The rule is: fold when displaying or grouping, never before a price lookup.
// Folding early breaks pricing, because a display name is not guaranteed to
// exist in the pricing table; folding late splits one model across two rows.
// Where both apply — the cache page, per-device cost — price each reported id
// first, then merge the results under the folded name.
//
// Fold before any LIMIT or top-N as well. Two variants that each place below
// the cut can outrank everything once added together, and separately they let
// one model compete against itself for a slot.
//
// Two places deliberately keep the reported id, and are commented as such:
// the event drill-down (looking at one event means seeing what was recorded)
// and the unpriced-model list (that string is what a pricing override must
// match). The proxy speed channel keeps it too, for a narrower reason —
// quantiles cannot be merged after the fact.

var (
	// Bedrock region prefixes: us.anthropic.claude-x → anthropic.claude-x
	bedrockRegion = regexp.MustCompile(`^(us|eu|apac|jp|au|ca|sa|global)\.`)
	// Bedrock version suffixes: claude-x-v1:0 → claude-x
	bedrockVersion = regexp.MustCompile(`-v\d+(:\d+)?$`)
	// A pinned release date, in the two shapes the vendors write it:
	// claude-haiku-4-5-20251001 and gpt-4o-2024-08-06. The alias without it
	// points at exactly this snapshot, so they are one model.
	//
	// The shape has to be exact. A model number looks the same as a date to a
	// looser rule — `claude-opus-4-5` ends in a dash and digits too, and eating
	// that would merge every Opus 4 into one row.
	releaseDate = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)
	// Reasoning-effort suffixes attached by hosts such as Antigravity (e.g.
	// gemini-4-neon-high / gemini-4-neon-xhigh, claude-opus-4-6-medium / -high)
	// or OpenAI o-series (o3-mini-high). Deliberately excludes "-max", which is
	// a real model tier (qwen-max, gpt-5.1-codex-max).
	reasoningEffort = regexp.MustCompile(`(?i)-(extra-high|xhigh|high|medium|low|minimal)$`)
)

// StripReasoningEffort removes a trailing reasoning-effort suffix while
// preserving model families whose real tier name ends in "-medium" (Mistral /
// Magistral). Returns id unchanged when no effort suffix applies.
func StripReasoningEffort(id string) string {
	loc := reasoningEffort.FindStringIndex(id)
	if loc == nil || loc[0] <= 0 {
		return id
	}
	base := id[:loc[0]]
	lowerBase := strings.ToLower(base)
	if lowerBase == "mistral" || lowerBase == "magistral" ||
		strings.HasPrefix(lowerBase, "mistral-") || strings.HasPrefix(lowerBase, "magistral-") {
		return id
	}
	return base
}

// CanonicalModel reduces a reported model id to the name shown and grouped by.
func CanonicalModel(id string) string {
	m := strings.TrimSpace(id)
	if m == "" {
		return ""
	}
	m = bedrockRegion.ReplaceAllString(m, "")
	m = strings.TrimPrefix(m, "anthropic.")
	// Vertex pins a date with @; the model is the same either way.
	if i := strings.Index(m, "@"); i > 0 {
		m = m[:i]
	}
	// A provider-qualified id such as anthropic/claude-x keeps only the model.
	if i := strings.LastIndex(m, "/"); i >= 0 && i < len(m)-1 {
		m = m[i+1:]
	}
	m = bedrockVersion.ReplaceAllString(m, "")
	// After the version suffix, so a Bedrock id carrying both
	// (claude-sonnet-4-20250514-v1:0) loses them in the order they were added.
	m = releaseDate.ReplaceAllString(m, "")
	// Strip reasoning-effort tiers (-high / -xhigh / -medium / -low / -minimal)
	// so runs of the same model at different effort levels aggregate together.
	m = StripReasoningEffort(m)
	if m == "" {
		return id // never turn a real id into nothing
	}
	return m
}

// sourceLabels renames collection sources for display. The stored value stays
// `claude-code` — this is only about reading well beside `codex`, which is one
// word.
var sourceLabels = map[string]string{
	"claude-code": "claude",
}

// CanonicalSource is the display name for a collection source.
func CanonicalSource(source string) string {
	if v, ok := sourceLabels[source]; ok {
		return v
	}
	return source
}
