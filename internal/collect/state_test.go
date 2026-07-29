package collect

import (
	"path/filepath"
	"testing"
)

// A rescan has to survive the process that ordered it: the reset is written to
// disk, so the pass that follows (and any restart after it) re-reads from zero.
func TestResetOffsetsClearsPositionsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit("/logs/a.jsonl", 4096, 1785319024000); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit("/logs/b.jsonl", 128, 0); err != nil {
		t.Fatal(err)
	}
	// A resolved repo, which the reset must not throw away.
	st.RepoFor("mac", "/src/omnitoken", func(string) string { return "local:OmniToken" })

	n, err := st.ResetOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("ResetOffsets reported %d files, want 2", n)
	}

	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Offset("/logs/a.jsonl"); got != 0 {
		t.Errorf("offset after reset = %d, want 0", got)
	}
	// The turn-start carry describes the boundary an offset sits at, so leaving
	// it behind would make the first re-read measure from the wrong instant.
	if got := reloaded.TurnStartFor("/logs/a.jsonl"); got != 0 {
		t.Errorf("turn start after reset = %d, want 0", got)
	}
	// The repo cache costs a git probe per directory and has nothing to do with
	// what the parsers derive from log bytes.
	probed := false
	repo := reloaded.RepoFor("mac", "/src/omnitoken", func(string) string {
		probed = true
		return "wrong"
	})
	if probed || repo != "local:OmniToken" {
		t.Errorf("repo cache lost by reset: repo=%q reprobed=%v", repo, probed)
	}
}
