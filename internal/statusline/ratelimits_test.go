package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of the channel: quota arrives on stdin with the rest of the
// payload, so rendering a status line is enough to capture it.
func TestRunCapturesRateLimitsFromPayload(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Server: "http://127.0.0.1:1", CachePath: filepath.Join(dir, "statusline-cache.json")}
	in := strings.NewReader(`{
	  "model":{"display_name":"Opus"},
	  "rate_limits":{
	    "five_hour":{"used_percentage":42,"resets_at":1774020000},
	    "seven_day":{"used_percentage":15,"resets_at":1774540000}}}`)

	var out strings.Builder
	if err := Run(cfg, in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Len() == 0 {
		t.Error("no status line printed; capture must not replace rendering")
	}

	raw, err := os.ReadFile(RateLimitsPath(cfg.CachePath))
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	var f RateLimitsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if f.Source != "claude-code" || f.ObservedAt <= 0 {
		t.Errorf("source/observed_at = %q/%d", f.Source, f.ObservedAt)
	}
	if got := f.Windows["five_hour"]; got.UsedPercent != 42 || got.ResetsAt != 1774020000 {
		t.Errorf("five_hour = %+v, want 42%% @1774020000 (seconds kept as-is)", got)
	}
	if _, ok := f.Windows["seven_day"]; !ok {
		t.Error("seven_day missing")
	}
	if _, ok := f.Windows["seven_day_opus"]; ok {
		t.Error("absent windows must not be invented")
	}
}

// A 0% window with no reset is a placeholder rather than an observation —
// writing it would show a freshly-reset quota that was never measured.
func TestCaptureSkipsPlaceholderWindows(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CachePath: filepath.Join(dir, "statusline-cache.json")}
	cfg.applyDefaults()

	var sess sessionInput
	if err := json.Unmarshal([]byte(`{"rate_limits":{
	  "five_hour":{"used_percentage":0,"resets_at":0},
	  "seven_day":{"used_percentage":3,"resets_at":1774540000}}}`), &sess); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	captureRateLimits(cfg, sess, time.Now())

	raw, err := os.ReadFile(RateLimitsPath(cfg.CachePath))
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	var f RateLimitsFile
	_ = json.Unmarshal(raw, &f)
	if _, ok := f.Windows["five_hour"]; ok {
		t.Error("placeholder five_hour was written")
	}
	if len(f.Windows) != 1 {
		t.Errorf("windows = %v, want only seven_day", f.Windows)
	}
}

// A payload without rate_limits must leave no file at all, rather than an
// empty one the collector would have to special-case.
func TestCaptureWritesNothingWithoutRateLimits(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CachePath: filepath.Join(dir, "statusline-cache.json")}
	cfg.applyDefaults()

	captureRateLimits(cfg, sessionInput{}, time.Now())
	if _, err := os.Stat(RateLimitsPath(cfg.CachePath)); !os.IsNotExist(err) {
		t.Errorf("file exists without rate_limits in the payload (err=%v)", err)
	}
}

// Capture exists so nobody has to give up their own status line to feed
// OmniToken: it must take the quota and stay silent.
func TestCaptureOnlyIsSilentButStillCaptures(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CachePath: filepath.Join(dir, "statusline-cache.json")}
	in := strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":88,"resets_at":1774020000}}}`)

	if err := Capture(cfg, in); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	raw, err := os.ReadFile(RateLimitsPath(cfg.CachePath))
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	var f RateLimitsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := f.Windows["five_hour"]; got.UsedPercent != 88 {
		t.Errorf("five_hour = %+v, want 88%%", got)
	}
}

func TestCaptureAntigravityQuotasToSeparateFile(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CachePath: filepath.Join(dir, "statusline-cache.json")}
	in := strings.NewReader(`{
		"product": "antigravity",
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.725, "reset_time": "2026-11-23T12:00:00Z"},
			"gemini-weekly": {"remaining_fraction": 0.9378, "reset_time": "2026-11-30T07:50:32Z"},
			"gemini-argon-wtu-weekly": {"remaining_fraction": 0.5, "reset_in_seconds": 86400},
			"3p-5h": {"remaining_fraction": 0.1, "reset_time": "2026-11-23T11:30:00Z"},
			"disabled-bucket": {"remaining_fraction": 0.0, "disabled": true}
		}
	}`)
	if err := Capture(cfg, in); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	// Must NOT touch Claude Code's rate-limits.json.
	if _, err := os.Stat(RateLimitsPath(cfg.CachePath)); !os.IsNotExist(err) {
		t.Errorf("Claude rate-limits.json should not be created for Antigravity payload")
	}
	raw, err := os.ReadFile(AntigravityRateLimitsPath(cfg.CachePath))
	if err != nil {
		t.Fatalf("read antigravity capture: %v", err)
	}
	var f RateLimitsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if f.Source != "antigravity" || f.ObservedAt <= 0 {
		t.Fatalf("source/observed = %q/%d", f.Source, f.ObservedAt)
	}
	if got := f.Windows["gemini-5h"]; got.UsedPercent != 27.5 || got.ResetsAt != 1795435200 {
		t.Errorf("gemini-5h = %+v, want 27.5%% @1795435200", got)
	}
	if got := f.Windows["gemini-weekly"]; got.UsedPercent != 6.2 {
		t.Errorf("gemini-weekly = %+v, want 6.2%%", got)
	}
	if got := f.Windows["gemini-argon-wtu-weekly"]; got.UsedPercent != 50 || got.ResetsAt <= 0 {
		t.Errorf("gemini-argon-wtu-weekly = %+v, want 50%% with computed reset", got)
	}
	if got := f.Windows["3p-5h"]; got.UsedPercent != 90 {
		t.Errorf("3p-5h = %+v, want 90%%", got)
	}
	if _, ok := f.Windows["disabled-bucket"]; ok {
		t.Errorf("disabled bucket must be skipped")
	}
}

func TestSetupTargetAntigravityWrapsAndRestoresPowerline(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "antigravity-cli", "settings.json")
	dataDir := filepath.Join(dir, "omnitoken")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	origCmd := `'/opt/homebrew/bin/node' '/Users/suool/.agy-powerline/statusline.js'`
	if err := os.WriteFile(settingsPath, []byte(`{"statusLine":{"type":"command","command":"`+origCmd+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1795430000, 0)
	res, err := SetupTarget(TargetAntigravity, settingsPath, dataDir, "/usr/local/bin/omnitoken", now)
	if err != nil {
		t.Fatalf("SetupTarget: %v", err)
	}
	if res.Wrapped != origCmd || !strings.HasSuffix(res.ScriptPath, antigravityHookScriptName) {
		t.Fatalf("unexpected setup result: %+v", res)
	}
	script, err := os.ReadFile(res.ScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), origCmd) || !strings.Contains(string(script), "statusline -capture-only") {
		t.Fatalf("wrapper script missing capture or wrapped command:\n%s", script)
	}
	restored, err := UndoTarget(TargetAntigravity, settingsPath, dataDir, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("UndoTarget: %v", err)
	}
	if restored != origCmd {
		t.Fatalf("restored = %q, want %q", restored, origCmd)
	}
}
