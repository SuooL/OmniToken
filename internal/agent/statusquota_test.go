package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/statusline"
)

// writeCapture drops the file `omnitoken statusline` leaves behind, beside the
// cache path the agent is configured with.
func writeCapture(t *testing.T, cachePath string, observed time.Time, resetsAtSeconds int64) {
	t.Helper()
	f := statusline.RateLimitsFile{
		Source:     "claude-code",
		ObservedAt: observed.UnixMilli(),
		Windows: map[string]statusline.RateLimitOn{
			"five_hour": {UsedPercent: 12, ResetsAt: resetsAtSeconds},
			"seven_day": {UsedPercent: 10, ResetsAt: resetsAtSeconds + 86400},
		},
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusline.RateLimitsPath(cachePath), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Claude's quota reaches OmniToken through the status line, which drops it in a
// file for a collector to pick up (ADR-0011). That collector only ever ran
// inside `serve`, so on the topology the deployment guide recommends — an agent
// on each machine reporting to a hub elsewhere — the reading never left the
// machine that took it. The panel said "no quota reported" while the capture
// file beside it was seconds old.
//
// Codex hid the hole: its quota comes out of the rollout logs the scan already
// parses, so one of the two sources kept working and the gap looked like a
// Claude-side outage rather than a missing collector.
func TestAgentReportsClaudeQuotaCapturedByTheStatusLine(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "statusline-cache.json")
	now := time.Now()
	writeCapture(t, cachePath, now, now.Add(2*time.Hour).Unix())

	var got struct {
		Quotas []model.QuotaSnapshot `json:"quotas"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	a, err := New(Config{
		ServerURL:           server.URL,
		Token:               "legacy-token",
		DeviceName:          "suool-mac",
		StatePath:           filepath.Join(dir, "state.json"),
		StatuslineCachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.collectStatusQuota(now); err != nil {
		t.Fatalf("collectStatusQuota: %v", err)
	}

	if len(got.Quotas) != 2 {
		t.Fatalf("reported %d quota snapshots, want 2 (five_hour + seven_day)", len(got.Quotas))
	}
	for _, q := range got.Quotas {
		if q.Source != "claude-code" {
			t.Errorf("source = %q, want claude-code", q.Source)
		}
		if q.Device != "suool-mac" {
			t.Errorf("device = %q, want suool-mac", q.Device)
		}
		// The capture holds epoch seconds; everything downstream is millis, and
		// a reading stamped in the wrong unit lands in 1970 and is dropped as
		// expired — indistinguishable, on the panel, from never having arrived.
		if q.ResetsAt < now.UnixMilli() {
			t.Errorf("resets_at = %d is not in the future; seconds/millis mix-up?", q.ResetsAt)
		}
	}
}

// A second pass over an unchanged file must stay quiet: ObservedAt means "we
// saw this then", not "we re-read a file", and the resident loop calls this on
// every tick.
func TestAgentDoesNotReportAnUnchangedCaptureTwice(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "statusline-cache.json")
	now := time.Now()
	writeCapture(t, cachePath, now, now.Add(2*time.Hour).Unix())

	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	a, err := New(Config{
		ServerURL:           server.URL,
		DeviceName:          "mac",
		StatePath:           filepath.Join(dir, "state.json"),
		StatuslineCachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := a.collectStatusQuota(now); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 {
		t.Errorf("posted %d times for one capture, want 1", posts)
	}
}

// The defect was never in the reader — it was that nobody constructed one. So
// the wiring itself is asserted: a configured agent must come out of New with a
// reader aimed at the file the status line actually writes.
func TestNewAgentWiresTheStatusLineQuotaReader(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "statusline-cache.json")

	a, err := New(Config{
		ServerURL:           "http://127.0.0.1:8787",
		DeviceName:          "mac",
		StatePath:           filepath.Join(dir, "state.json"),
		StatuslineCachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.quotaReader == nil {
		t.Fatal("agent has no status-line quota reader; Claude quota would never leave this machine")
	}
	if want := statusline.RateLimitsPath(cachePath); a.quotaReader.Path() != want {
		t.Errorf("reader path = %q, want %q", a.quotaReader.Path(), want)
	}
}

// With no cache path there is nothing to read, and `RateLimitsPath("")` would
// resolve to "./rate-limits.json" — relative to whatever directory the agent
// happens to have been started from. Reading the wrong file is worse than
// reading none, so the reader is simply not built. Filling the default belongs
// to the caller, which knows the data directory; this package must not reach
// for `internal/server` to find it.
func TestNoQuotaReaderWithoutACachePath(t *testing.T) {
	a, err := New(Config{
		ServerURL:  "http://127.0.0.1:8787",
		DeviceName: "mac",
		StatePath:  filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.quotaReader != nil {
		t.Errorf("built a reader from an empty cache path: %q", a.quotaReader.Path())
	}
	// And collecting is a no-op rather than a crash.
	if err := a.collectStatusQuota(time.Now()); err != nil {
		t.Errorf("collectStatusQuota with no reader: %v", err)
	}
}
