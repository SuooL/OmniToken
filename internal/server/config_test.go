package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDefaultConfigRoundTrips(t *testing.T) {
	// Nested dir that does not exist yet: the writer owns creating it.
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := WriteDefaultConfig(path); err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}

	// Loading the generated file must produce the same settings as running
	// with no config at all — generating a file must not change behaviour.
	fromFile, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pureDefaults, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig(\"\"): %v", err)
	}

	if fromFile.Listen != pureDefaults.Listen {
		t.Errorf("Listen = %q, want %q", fromFile.Listen, pureDefaults.Listen)
	}
	if fromFile.DBPath != pureDefaults.DBPath {
		t.Errorf("DBPath = %q, want %q", fromFile.DBPath, pureDefaults.DBPath)
	}
	if fromFile.Collect.IntervalSeconds != pureDefaults.Collect.IntervalSeconds {
		t.Errorf("IntervalSeconds = %d, want %d",
			fromFile.Collect.IntervalSeconds, pureDefaults.Collect.IntervalSeconds)
	}
	if !fromFile.LocalEnabled() {
		t.Error("LocalEnabled() = false, want true")
	}

	// `"local": null` would read like a bug; it must be spelled out.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc struct {
		Collect struct {
			Local *bool `json:"local"`
		} `json:"collect"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}
	if doc.Collect.Local == nil {
		t.Error(`generated config has "local": null, want an explicit true`)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %#o, want 0600 (the file carries the ingest token)", perm)
	}
}

func TestWriteDefaultConfigNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"listen":":9999","token":"secret"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteDefaultConfig(path); err == nil {
		t.Fatal("WriteDefaultConfig overwrote an existing config; it must refuse")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("existing config was modified:\n got %s\nwant %s", got, original)
	}
}
