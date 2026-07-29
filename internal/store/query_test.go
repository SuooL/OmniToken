package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// One model arriving under two ids must total as one row, and the fold has to
// happen before the limit or a variant can be cut while its sibling survives.
func TestBreakdownFoldsModelVariants(t *testing.T) {
	st, err := Open(t.TempDir() + "/b.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Now()
	evs := []model.Event{
		{EventID: "a", TS: now.UnixMilli(), Device: "d", Source: "claude-code",
			Model: "claude-opus-4-8", Provider: "anthropic", InputTokens: 100, OutputTokens: 10},
		{EventID: "b", TS: now.UnixMilli(), Device: "d", Source: "claude-code",
			Model: "anthropic.claude-opus-4-8", Provider: "bedrock", InputTokens: 200, OutputTokens: 20},
		{EventID: "c", TS: now.UnixMilli(), Device: "d", Source: "codex",
			Model: "gpt-5.6-sol", Provider: "openai", InputTokens: 50, OutputTokens: 5},
	}
	if _, err := st.InsertEvents(evs, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rows, err := st.Breakdown("model", now.Add(-time.Hour), now.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Key] = r.TotalTokens
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d (%v), want 2 after folding", len(rows), got)
	}
	if want := int64(100 + 10 + 200 + 20); got["claude-opus-4-8"] != want {
		t.Errorf("claude-opus-4-8 = %d, want %d (both ids merged)", got["claude-opus-4-8"], want)
	}
	// The family name stays, so an OpenAI model is untouched.
	if got["gpt-5.6-sol"] != 55 {
		t.Errorf("gpt-5.6-sol = %d, want 55", got["gpt-5.6-sol"])
	}
}
