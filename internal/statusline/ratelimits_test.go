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
