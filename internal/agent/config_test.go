package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSkeletonConfigCreatesEditableFile(t *testing.T) {
	// Nested dir that does not exist yet: the skeleton writer owns creating it.
	path := filepath.Join(t.TempDir(), "nested", "agent.json")

	if err := WriteSkeletonConfig(path); err != nil {
		t.Fatalf("WriteSkeletonConfig: %v", err)
	}

	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	// Server must stay empty: a placeholder host would turn the next run into
	// a connection failure instead of the actionable "server is required".
	if fc.Server != "" {
		t.Errorf("Server = %q, want empty so the required-field error repeats", fc.Server)
	}
	if fc.IntervalSeconds != 15 {
		t.Errorf("IntervalSeconds = %d, want 15", fc.IntervalSeconds)
	}

	// The file carries a token once filled in — it must not be world-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %#o, want 0600", perm)
	}

	// "server" must be present in the JSON, otherwise the user cannot tell
	// which key to fill in.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}
	if _, ok := keys["server"]; !ok {
		t.Error(`generated skeleton has no "server" key`)
	}
}

func TestWriteSkeletonConfigNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	original := []byte(`{"server":"http://kept:8787","token":"secret"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteSkeletonConfig(path); err == nil {
		t.Fatal("WriteSkeletonConfig overwrote an existing config; it must refuse")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("existing config was modified:\n got %s\nwant %s", got, original)
	}
}
