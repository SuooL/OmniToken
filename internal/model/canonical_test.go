package model

import "testing"

func TestCanonicalModelStripsVendorRouting(t *testing.T) {
	cases := map[string]string{
		// The case that started this: one model, two ids.
		"claude-opus-4-8":                            "claude-opus-4-8",
		"anthropic.claude-opus-4-8":                  "claude-opus-4-8",
		"us.anthropic.claude-opus-4-8":               "claude-opus-4-8",
		"us.anthropic.claude-sonnet-4-20250514-v1:0": "claude-sonnet-4-20250514",
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
