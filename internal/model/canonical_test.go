package model

import "testing"

func TestCanonicalModelStripsVendorRouting(t *testing.T) {
	cases := map[string]string{
		// The case that started this: one model, two ids.
		"claude-opus-4-8":                            "claude-opus-4-8",
		"anthropic.claude-opus-4-8":                  "claude-opus-4-8",
		"us.anthropic.claude-opus-4-8":               "claude-opus-4-8",
		"us.anthropic.claude-sonnet-4-20250514-v1:0": "claude-sonnet-4",
		"anthropic/claude-opus-4-8":                  "claude-opus-4-8",
		// Vertex pins a date; same model.
		"claude-opus-4-8@20250514": "claude-opus-4-8",
	}
	for in, want := range cases {
		if got := CanonicalModel(in); got != want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pinned snapshot and its alias are one model. Anthropic's own ids carry the
// release date (`claude-haiku-4-5-20251001`) while the tool may report the
// alias (`claude-haiku-4-5`), and both turned up in the same database — three
// rows for one model once the Bedrock prefix is counted.
func TestCanonicalModelStripsPinnedReleaseDates(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001":           "claude-haiku-4-5",
		"anthropic.claude-haiku-4-5-20251001": "claude-haiku-4-5",
		"anthropic.claude-haiku-4-5":          "claude-haiku-4-5",
		"claude-opus-4-5-20251101":            "claude-opus-4-5",
		// OpenAI writes the same thing with separators.
		"gpt-4o-2024-08-06": "gpt-4o",
	}
	for in, want := range cases {
		if got := CanonicalModel(in); got != want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A version is not a date. These differ from a pinned snapshot only in how many
// digits follow the last dash, so the rule has to be exact about the shape or
// it eats the model number itself.
func TestCanonicalModelKeepsVersionNumbers(t *testing.T) {
	for _, id := range []string{
		"claude-opus-4-5",   // the alias the dates above fold onto
		"claude-sonnet-4-6", // a minor version, not a year
		"gpt-5.4",
		"gpt-4o-2024", // too short to be a date
	} {
		if got := CanonicalModel(id); got != id {
			t.Errorf("CanonicalModel(%q) = %q, want it unchanged", id, got)
		}
	}
}

// The family name is what makes a mixed list readable — stripping it would
// leave bare version numbers.
func TestCanonicalModelKeepsFamilyNames(t *testing.T) {
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.4", "claude-fable-5", "codex-auto-review"} {
		if got := CanonicalModel(id); got != id {
			t.Errorf("CanonicalModel(%q) = %q, want it unchanged", id, got)
		}
	}
}

// A rule that can empty out a real id would silently merge unrelated rows.
func TestCanonicalModelNeverReturnsEmptyForRealInput(t *testing.T) {
	for _, id := range []string{"anthropic.", "us.", "-v1:0", "  "} {
		if got := CanonicalModel(id); got == "" && id != "  " {
			t.Errorf("CanonicalModel(%q) emptied a non-empty id", id)
		}
	}
	if got := CanonicalModel(""); got != "" {
		t.Errorf("CanonicalModel(\"\") = %q, want empty", got)
	}
}

func TestCanonicalSource(t *testing.T) {
	if got := CanonicalSource("claude-code"); got != "claude" {
		t.Errorf("CanonicalSource(claude-code) = %q, want claude", got)
	}
	// Anything without a mapping passes through, so a new source is never
	// silently renamed to something wrong.
	if got := CanonicalSource("codex"); got != "codex" {
		t.Errorf("CanonicalSource(codex) = %q, want codex", got)
	}
}
