package collect

import (
	"os"
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

func TestCommitAndResetClearInFlightDeliveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	file := "/logs/a.jsonl"
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BeginScan(file, 0, 128, 42); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDeliveryDone(file, "events:0:digest"); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(file, 128, 42); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.InFlightFor(file); ok {
		t.Fatal("Commit retained an in-flight delivery ledger")
	}

	if err := reloaded.BeginScan(file, 0, 256, 84); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.MarkDeliveryDone(file, "events:0:other"); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.ResetOffsets(); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.InFlightFor(file); ok {
		t.Fatal("ResetOffsets retained an in-flight delivery ledger")
	}
}

func TestLoadStateDiscardsInvalidInFlightBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{
	  "files": {"/logs/a.jsonl": 64},
	  "repo_by_cwd": {},
	  "turn_start": {},
	  "in_flight": {
	    "/logs/a.jsonl": {
	      "start": 128,
	      "end": 32,
	      "delivered": {"events:0:untrusted": true}
	    }
	  }
	}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.InFlightFor("/logs/a.jsonl"); ok {
		t.Fatal("invalid in-flight boundary was trusted")
	}
	if got := st.Offset("/logs/a.jsonl"); got != 64 {
		t.Fatalf("valid committed offset = %d, want 64", got)
	}
}
