package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgentConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The window is what stops a fresh agent on a machine with a synced home
// directory from claiming another machine's history — and a push carries
// self-report authority, so that claim would win (ADR-0015).
func TestSinceTimeIsLocalMidnight(t *testing.T) {
	got, err := FileConfig{Since: "2026-07-27"}.SinceTime()
	if err != nil {
		t.Fatalf("SinceTime: %v", err)
	}
	want := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("SinceTime() = %v, want %v", got, want)
	}
}

// Absent means "report everything" — which is what every existing agent.json
// says by saying nothing.
func TestSinceEmptyMeansNoWindow(t *testing.T) {
	got, err := FileConfig{}.SinceTime()
	if err != nil {
		t.Fatalf("SinceTime: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("SinceTime() = %v, want the zero time", got)
	}
}

// A typo must stop the agent at load. Treating it as "no window" would push
// exactly the history the user asked to leave out, and attribution has no undo.
func TestLoadFileConfigRejectsMalformedSince(t *testing.T) {
	for _, bad := range []string{"2026-13-01", "07/27/2026", "last tuesday", "2026-07"} {
		path := writeAgentConfig(t, `{"server":"http://x:1","since":"`+bad+`"}`)
		if _, err := LoadFileConfig(path); err == nil {
			t.Errorf("since=%q loaded without error, want a rejection", bad)
		}
	}
}

func TestLoadFileConfigAcceptsAWellFormedSince(t *testing.T) {
	path := writeAgentConfig(t, `{"server":"http://x:1","since":"2026-07-27","name":"hzsmini"}`)
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if fc.Since != "2026-07-27" || fc.Name != "hzsmini" {
		t.Errorf("got %+v", fc)
	}
}

// A missing file stays a non-error — that is how the agent bootstraps.
func TestLoadFileConfigMissingFileIsNotAnError(t *testing.T) {
	fc, err := LoadFileConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if fc.Since != "" || fc.Server != "" {
		t.Errorf("got %+v, want the zero config", fc)
	}
}

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
	if fc.IntervalSeconds != 10 {
		t.Errorf("IntervalSeconds = %d, want 10", fc.IntervalSeconds)
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

func TestFileConfigDefaultsExistingFilesToProtocolV1(t *testing.T) {
	path := writeAgentConfig(t, `{"server":"http://x:1","token":"legacy"}`)
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fc.EffectiveProtocolVersion(); got != 1 {
		t.Fatalf("effective protocol = %d, want 1", got)
	}
}

func TestLoadFileConfigAcceptsProtocolV2Settings(t *testing.T) {
	path := writeAgentConfig(t, `{
		"server":"http://x:1",
		"allow_insecure_http":true,
		"protocol_version":2,
		"device_id":"018f2d5a-7b31-7d98-bf8e-3c2f35a1a001",
		"device_token":"device-secret",
		"outbox":"/tmp/omnitoken-outbox.db",
		"outbox_max_bytes":12345,
		"relay_token":"relay-secret",
		"relay_upstream_token":"next-relay-secret"
	}`)
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if fc.EffectiveProtocolVersion() != 2 || fc.DeviceToken != "device-secret" ||
		fc.Outbox != "/tmp/omnitoken-outbox.db" || fc.OutboxMaxBytes != 12345 ||
		!fc.AllowInsecureHTTP || fc.RelayToken != "relay-secret" ||
		fc.RelayUpstreamToken != "next-relay-secret" {
		t.Fatalf("loaded config = %+v", fc)
	}
}

func TestLoadFileConfigRejectsUnsupportedProtocol(t *testing.T) {
	path := writeAgentConfig(t, `{"server":"http://x:1","protocol_version":3}`)
	if _, err := LoadFileConfig(path); err == nil {
		t.Fatal("unsupported protocol loaded without error")
	}
}

func TestPrepareEnrollmentCreatesStableIdentityAndRenamePreservesIt(t *testing.T) {
	first, err := PrepareEnrollment(FileConfig{IntervalSeconds: 15}, "https://hub.example", "Before", "fixed-device-token")
	if err != nil {
		t.Fatal(err)
	}
	if first.ProtocolVersion != 2 || first.DeviceID == "" || first.DeviceToken != "fixed-device-token" {
		t.Fatalf("first enrollment config = %+v", first)
	}

	renamed, err := PrepareEnrollment(first, "https://hub.example", "After", "")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DeviceID != first.DeviceID || renamed.DeviceToken != first.DeviceToken {
		t.Fatalf("rename changed identity: before=%+v after=%+v", first, renamed)
	}
	if renamed.Name != "After" {
		t.Fatalf("renamed display name = %q, want After", renamed.Name)
	}
}

func TestSaveFileConfigPersistsEnrollmentWithMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "agent.json")
	fc, err := PrepareEnrollment(FileConfig{}, "https://hub.example", "Agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveFileConfig(path, fc); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != fc.DeviceID || loaded.DeviceToken != fc.DeviceToken ||
		loaded.ProtocolVersion != 2 || loaded.Server != "https://hub.example" {
		t.Fatalf("loaded enrollment = %+v, want %+v", loaded, fc)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
}
