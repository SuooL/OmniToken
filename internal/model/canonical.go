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
// Only vendor routing prefixes are stripped. The family name stays: `claude-`
// and `gpt-` say what something is, and a list of bare version numbers would
// be harder to read, not easier.
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
)

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
