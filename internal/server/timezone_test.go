package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A day boundary is what turns an epoch into "today". Leaving it to whatever
// timezone the host happens to be set to means a laptop crossing an ocean
// silently re-buckets every daily number — measured on the real database, the
// same instant yields 1,954 events under America/New_York and 4,109 under
// Asia/Shanghai. The config has to be able to pin it.
func TestConfigResolvesTimezone(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"timezone":"Asia/Shanghai"}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	loc := cfg.Location()
	if loc == nil {
		t.Fatal("Location() is nil")
	}
	if loc.String() != "Asia/Shanghai" {
		t.Errorf("Location() = %s, want Asia/Shanghai", loc)
	}
}

// A typo must not degrade into a different but plausible-looking answer. Same
// stance as ADR-0015 took for `since`: a malformed value that silently becomes
// "no window" imports history the user asked to skip, and that is not
// reversible. Here the damage is quieter — every daily bucket shifts — so the
// load has to fail loudly.
func TestConfigRejectsUnknownTimezone(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, `{"timezone":"Nowhere/Atlantis"}`))
	if err == nil {
		t.Fatal("expected an unknown timezone to be rejected at load")
	}
	if !strings.Contains(err.Error(), "timezone") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

// Empty stays valid: requiring it would add a step to every headless install
// (N1). It resolves to the host zone — the point of the field is that the
// choice becomes recordable, not that it becomes mandatory.
func TestConfigWithoutTimezoneFollowsHost(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Location(); got != time.Local {
		t.Errorf("Location() = %v, want time.Local", got)
	}
}

// The reason ADR-0021 assigns `time.Local` instead of threading a Location
// through every caller: it is the one switch that also reaches the eight SQL
// sites using `date(..., 'localtime')`, which the modernc driver resolves via
// `time.Local`. If that ever stops holding, the Go and SQL halves of a single
// page would disagree about which day a row belongs to — so assert both halves
// move together rather than trusting the driver's current behaviour.
func TestApplyTimezoneMovesBothDayBoundaries(t *testing.T) {
	restore := time.Local
	t.Cleanup(func() { time.Local = restore })

	cfg, err := LoadConfig(writeConfig(t, `{"timezone":"Asia/Tokyo"}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := ApplyTimezone(cfg); got != "Asia/Tokyo" {
		t.Fatalf("ApplyTimezone reported %q, want Asia/Tokyo", got)
	}
	if time.Local.String() != "Asia/Tokyo" {
		t.Fatalf("time.Local = %s; the SQL half follows this, so it must move", time.Local)
	}

	// An instant chosen so the two zones disagree about the date: 22:00 UTC is
	// already tomorrow in Tokyo (+09:00) and still today in New York (-04:00).
	instant := time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC)
	if got := instant.In(time.Local).Format("2006-01-02"); got != "2026-08-01" {
		t.Errorf("Go side bucketed %s into %s, want 2026-08-01", instant, got)
	}

	ny, _ := time.LoadLocation("America/New_York")
	if got := instant.In(ny).Format("2006-01-02"); got != "2026-07-31" {
		t.Fatalf("test premise broken: %s is %s in New York", instant, got)
	}
}
