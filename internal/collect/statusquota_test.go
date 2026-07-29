package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCapture(t *testing.T, dir, body string) string {
	t.Helper()
	cache := filepath.Join(dir, "statusline-cache.json")
	if err := os.WriteFile(filepath.Join(dir, "rate-limits.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	return cache
}

func TestStatusQuotaReaderMapsWindows(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// resets_at is epoch SECONDS in the payload; snapshots carry milliseconds.
	body := `{"source":"claude-code","observed_at":` + itoa(now.UnixMilli()) + `,
	  "windows":{
	    "five_hour":{"used_percent":42,"resets_at":1774020000},
	    "seven_day":{"used_percent":15,"resets_at":1774540000},
	    "seven_day_opus":{"used_percent":7,"resets_at":1774540000}}}`
	cache := writeCapture(t, dir, body)

	got := NewStatusQuotaReader("mac", cache).Collect(now)
	if len(got) != 3 {
		t.Fatalf("got %d snapshots, want 3", len(got))
	}
	by := map[string]int{}
	for i, q := range got {
		by[q.Scope] = i
		if q.Device != "mac" || q.Source != "claude-code" {
			t.Errorf("scope %s: device/source = %s/%s", q.Scope, q.Device, q.Source)
		}
	}
	five := got[by["five_hour"]]
	if five.WindowMinutes != 300 {
		t.Errorf("five_hour window = %d, want 300", five.WindowMinutes)
	}
	if five.UsedPercent != 42 {
		t.Errorf("five_hour used = %v, want 42", five.UsedPercent)
	}
	if five.ResetsAt != 1774020000*1000 {
		t.Errorf("five_hour resets_at = %d, want seconds converted to ms", five.ResetsAt)
	}
	if got[by["seven_day"]].WindowMinutes != 10080 {
		t.Errorf("seven_day window = %d, want 10080", got[by["seven_day"]].WindowMinutes)
	}
	// Per-model weekly must survive as its own scope — collapsing it onto
	// seven_day is the bug ADR-0007 had to fix once already.
	if _, ok := by["seven_day_opus"]; !ok {
		t.Error("seven_day_opus was dropped; per-model weekly must stay distinct")
	}
}

// Re-reading the same capture must not keep re-reporting it: ObservedAt means
// "we saw this then", not "we polled a file again".
func TestStatusQuotaReaderReportsEachCaptureOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	cache := writeCapture(t, dir, `{"observed_at":`+itoa(now.UnixMilli())+
		`,"windows":{"five_hour":{"used_percent":10,"resets_at":1774020000}}}`)

	r := NewStatusQuotaReader("mac", cache)
	if n := len(r.Collect(now)); n != 1 {
		t.Fatalf("first read gave %d, want 1", n)
	}
	if n := len(r.Collect(now)); n != 0 {
		t.Errorf("second read of an unchanged capture gave %d, want 0", n)
	}

	// A fresh capture is reported again.
	writeCapture(t, dir, `{"observed_at":`+itoa(now.Add(time.Minute).UnixMilli())+
		`,"windows":{"five_hour":{"used_percent":11,"resets_at":1774020000}}}`)
	if n := len(r.Collect(now)); n != 1 {
		t.Errorf("new capture gave %d, want 1", n)
	}
}

// The status line only runs while Claude Code does, so an old capture
// describes a window that has almost certainly rolled over.
func TestStatusQuotaReaderDropsStaleCapture(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-13 * time.Hour).UnixMilli()
	cache := writeCapture(t, dir, `{"observed_at":`+itoa(old)+
		`,"windows":{"five_hour":{"used_percent":90,"resets_at":1774020000}}}`)

	if n := len(NewStatusQuotaReader("mac", cache).Collect(now)); n != 0 {
		t.Errorf("stale capture gave %d snapshots, want 0", n)
	}
}

// Not having run the status line yet is the normal state, not an error.
func TestStatusQuotaReaderToleratesMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "statusline-cache.json")
	if n := len(NewStatusQuotaReader("mac", cache).Collect(time.Now())); n != 0 {
		t.Errorf("missing file gave %d snapshots, want 0", n)
	}
	writeCapture(t, dir, `{"observed_at": not json`)
	if n := len(NewStatusQuotaReader("mac", cache).Collect(time.Now())); n != 0 {
		t.Errorf("malformed file gave %d snapshots, want 0", n)
	}
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{digits[v%10]}, b...)
		v /= 10
	}
	return string(b)
}
