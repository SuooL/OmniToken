package collect

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// A start window keeps an old machine's back catalogue out of the database
// (ADR-0015). The boundary instant belongs to the window: "since 2026-07-27"
// means from that day's first millisecond on.
func TestScanSourcesStartWindow(t *testing.T) {
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	parsed := []model.Event{
		{EventID: "before", TS: start.Add(-time.Millisecond).UnixMilli()},
		{EventID: "boundary", TS: start.UnixMilli()},
		{EventID: "after", TS: start.Add(36 * time.Hour).UnixMilli()},
	}

	cases := []struct {
		name      string
		notBefore time.Time
		want      []string
	}{
		{"no window ingests everything", time.Time{}, []string{"before", "boundary", "after"}},
		{"a window drops what predates it", start, []string{"boundary", "after"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "rollout.jsonl")
			if err := os.WriteFile(logPath, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			spec := SourceSpec{Dirs: []string{dir}, Parse: func(r io.Reader, device string, turnStartMS int64) model.ParseResult {
				b, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				return model.ParseResult{
					Events:   append([]model.Event(nil), parsed...),
					Consumed: int64(len(b)),
				}
			}}
			st, err := LoadState(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			sink := func(events []model.Event) error {
				for _, e := range events {
					got = append(got, e.EventID)
				}
				return nil
			}

			n, err := ScanSources([]SourceSpec{spec}, "macmini", st, nil, sink, nil, tc.notBefore)
			if err != nil {
				t.Fatalf("ScanSources: %v", err)
			}
			if n != len(tc.want) {
				t.Errorf("reported %d events, want %d", n, len(tc.want))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("sink got %v, want %v", got, tc.want)
			}
			for i, id := range tc.want {
				if got[i] != id {
					t.Fatalf("sink got %v, want %v", got, tc.want)
				}
			}
			// The dropped lines were read, not deferred: the offset covers them,
			// so the next pass does not hand them to the sink a second time.
			if off := st.Offset(logPath); off != 3 {
				t.Errorf("offset = %d, want 3 (the whole file)", off)
			}
		})
	}
}

// A per-host start date is config, so a typo must be loud rather than silently
// meaning "no window" — that would import the history it was written to skip.
func TestSSHHostSinceTime(t *testing.T) {
	if got, err := (SSHHost{Host: "macmini"}).SinceTime(); err != nil || !got.IsZero() {
		t.Errorf("empty since = %v/%v, want zero time and no error", got, err)
	}
	got, err := (SSHHost{Host: "macmini", Since: "2026-07-27"}).SinceTime()
	if err != nil {
		t.Fatalf("SinceTime: %v", err)
	}
	if want := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("since = %v, want %v (local midnight)", got, want)
	}
	if _, err := (SSHHost{Host: "macmini", Since: "07/27/2026"}).SinceTime(); err == nil {
		t.Error("a malformed since was accepted; it must be rejected")
	}
}
