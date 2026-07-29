package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func statusCmd(t *testing.T, settings map[string]any) string {
	t.Helper()
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := sl["command"].(string)
	return c
}

// The whole point: someone already using another status line must keep it.
// abtop replaces the command outright, which blanks the bar.
func TestSetupWrapsAnExistingStatusLine(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")
	if err := os.WriteFile(settings, []byte(
		`{"statusLine":{"type":"command","command":"ccstatusline","padding":0},"other":{"keep":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Setup(settings, data, "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.Wrapped != "ccstatusline" {
		t.Errorf("Wrapped = %q, want ccstatusline", res.Wrapped)
	}

	after := readJSON(t, settings)
	if got := statusCmd(t, after); got != res.ScriptPath {
		t.Errorf("command = %q, want the wrapper %q", got, res.ScriptPath)
	}
	// Unrelated settings, and the statusLine's own options, must survive.
	if _, ok := after["other"]; !ok {
		t.Error("unrelated key was dropped")
	}
	if sl := after["statusLine"].(map[string]any); sl["padding"] == nil {
		t.Error("statusLine.padding was dropped")
	}

	script, err := os.ReadFile(res.ScriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	body := string(script)
	if !strings.Contains(body, "ccstatusline") {
		t.Error("wrapper does not call the original command")
	}
	if !strings.Contains(body, "statusline -capture-only") {
		t.Error("wrapper does not capture quota")
	}
	if !strings.Contains(body, "input=$(cat)") {
		t.Error("wrapper must read stdin once and share it")
	}
	if st, _ := os.Stat(res.ScriptPath); st.Mode().Perm()&0o111 == 0 {
		t.Error("wrapper is not executable")
	}
}

func TestSetupRegistersDirectlyWhenSlotIsFree(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")

	res, err := Setup(settings, data, "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.ScriptPath != "" {
		t.Errorf("wrapper written for an empty slot: %s", res.ScriptPath)
	}
	if got := statusCmd(t, readJSON(t, settings)); got != "/opt/omnitoken statusline" {
		t.Errorf("command = %q, want the binary directly", got)
	}
}

// Running setup twice must not stack wrappers around wrappers.
func TestSetupIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")
	os.WriteFile(settings, []byte(`{"statusLine":{"command":"ccstatusline"}}`), 0o644)

	first, err := Setup(settings, data, "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Setup(settings, data, "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyDone {
		t.Error("second run did not report AlreadyDone")
	}
	if got := statusCmd(t, readJSON(t, settings)); got != first.ScriptPath {
		t.Errorf("command changed on the second run: %q", got)
	}
	body, _ := os.ReadFile(first.ScriptPath)
	if strings.Count(string(body), "capture-only") != 1 {
		t.Error("wrapper was nested inside itself")
	}
}

// Undo is what abtop never shipped.
func TestUndoRestoresTheOriginal(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")
	os.WriteFile(settings, []byte(`{"statusLine":{"command":"ccstatusline","padding":0}}`), 0o644)

	res, err := Setup(settings, data, "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Undo(settings, data, time.Now())
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if restored != "ccstatusline" {
		t.Errorf("restored %q, want ccstatusline", restored)
	}
	if got := statusCmd(t, readJSON(t, settings)); got != "ccstatusline" {
		t.Errorf("command = %q after undo", got)
	}
	if _, err := os.Stat(res.ScriptPath); !os.IsNotExist(err) {
		t.Error("wrapper script left behind after undo")
	}
}

// Undoing an install that put OmniToken in an empty slot should leave the slot
// empty again, not leave a dangling entry.
func TestUndoRemovesEntryWhenSlotWasEmpty(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	data := filepath.Join(dir, "data")

	if _, err := Setup(settings, data, "/opt/omnitoken", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(settings, data, time.Now()); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, ok := readJSON(t, settings)["statusLine"]; ok {
		t.Error("statusLine entry survived undo of an empty-slot install")
	}
}

// Rewriting JSON we failed to parse would destroy settings that have nothing
// to do with us.
func TestSetupRefusesToRewriteMalformedSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	original := `{"statusLine": {"command": "ccstatusline",}}` // trailing comma
	os.WriteFile(settings, []byte(original), 0o644)

	if _, err := Setup(settings, filepath.Join(dir, "data"), "/opt/omnitoken", time.Now()); err == nil {
		t.Fatal("Setup accepted malformed JSON")
	}
	raw, _ := os.ReadFile(settings)
	if string(raw) != original {
		t.Error("malformed settings were modified anyway")
	}
}

func TestSetupBacksUpBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	original := `{"statusLine":{"command":"ccstatusline"}}`
	os.WriteFile(settings, []byte(original), 0o644)

	res, err := Setup(settings, filepath.Join(dir, "data"), "/opt/omnitoken", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupPath == "" {
		t.Fatal("no backup recorded")
	}
	raw, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(raw) != original {
		t.Errorf("backup = %q, want the original bytes", raw)
	}
}
