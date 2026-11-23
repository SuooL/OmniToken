package collect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func fixedScanSpec(dir string, events []model.Event, quotas []model.QuotaSnapshot, fullReparse bool) SourceSpec {
	return SourceSpec{
		Dirs:        []string{dir},
		FullReparse: fullReparse,
		Parse: func(r io.Reader, _ string, _ int64) model.ParseResult {
			data, _ := io.ReadAll(r)
			return model.ParseResult{
				Events:   append([]model.Event(nil), events...),
				Quotas:   append([]model.QuotaSnapshot(nil), quotas...),
				Consumed: int64(len(data)),
			}
		},
	}
}

func numberedEvents(n int) []model.Event {
	events := make([]model.Event, n)
	for i := range events {
		events[i] = model.Event{EventID: fmt.Sprintf("event-%d", i)}
	}
	return events
}

func writeScanFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollout.jsonl")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, logPath, statePath
}

func loadScanState(t *testing.T, path string) *State {
	t.Helper()
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestScanSourcesResumesDurableBatchesWithSingleBatchCapacity(t *testing.T) {
	dir, logPath, statePath := writeScanFixture(t)
	spec := fixedScanSpec(dir, numberedEvents(4001), nil, false)
	errFull := errors.New("one-batch capacity is full")
	occupied := false
	var accepted []string
	sink := func(events []model.Event) error {
		if occupied {
			return errFull
		}
		occupied = true
		accepted = append(accepted, events[0].EventID)
		return nil
	}

	st := loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); !errors.Is(err, errFull) {
		t.Fatalf("first scan error = %v, want capacity error", err)
	}
	if got := st.Offset(logPath); got != 0 {
		t.Fatalf("offset after partial enqueue = %d, want 0", got)
	}

	for attempt := 0; attempt < 2; attempt++ {
		occupied = false // the hub acknowledged the one queued batch
		st = loadScanState(t, statePath)
		_, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{})
		if attempt == 0 && !errors.Is(err, errFull) {
			t.Fatalf("second scan error = %v, want capacity error", err)
		}
		if attempt == 1 && err != nil {
			t.Fatalf("final scan: %v", err)
		}
	}

	want := []string{"event-0", "event-2000", "event-4000"}
	if fmt.Sprint(accepted) != fmt.Sprint(want) {
		t.Fatalf("accepted batch starts = %v, want %v", accepted, want)
	}
	if got := st.Offset(logPath); got != 3 {
		t.Fatalf("final offset = %d, want 3", got)
	}
}

func TestScanSourcesAlreadyDurableBatchDoesNotConsumeCapacityOnRescan(t *testing.T) {
	dir, logPath, statePath := writeScanFixture(t)
	spec := fixedScanSpec(dir, numberedEvents(4000), nil, false)
	errFull := errors.New("one-batch capacity is full")
	occupied := false
	var attempts []string
	sink := func(events []model.Event) error {
		attempts = append(attempts, events[0].EventID)
		if occupied {
			return errFull
		}
		occupied = true
		return nil
	}

	st := loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); !errors.Is(err, errFull) {
		t.Fatalf("first scan error = %v, want capacity error", err)
	}
	st = loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); !errors.Is(err, errFull) {
		t.Fatalf("rescan error = %v, want capacity error", err)
	}

	want := []string{"event-0", "event-2000", "event-2000"}
	if fmt.Sprint(attempts) != fmt.Sprint(want) {
		t.Fatalf("sink attempts = %v, want %v; durable first batch was retried", attempts, want)
	}
	if got := st.Offset(logPath); got != 0 {
		t.Fatalf("offset after incomplete rescan = %d, want 0", got)
	}
}

func TestScanSourcesQuotaSinkFailureDoesNotAdvanceOffset(t *testing.T) {
	dir, logPath, statePath := writeScanFixture(t)
	quota := model.QuotaSnapshot{Device: "device", Source: "codex", LimitID: "weekly", ObservedAt: 123}
	spec := fixedScanSpec(dir, nil, []model.QuotaSnapshot{quota}, false)
	errQuota := errors.New("quota enqueue failed")
	quotaCalls := 0
	quotaSink := func([]model.QuotaSnapshot) error {
		quotaCalls++
		if quotaCalls == 1 {
			return errQuota
		}
		return nil
	}

	st := loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, nil, quotaSink, time.Time{}); !errors.Is(err, errQuota) {
		t.Fatalf("first scan error = %v, want quota error", err)
	}
	if got := st.Offset(logPath); got != 0 {
		t.Fatalf("offset after quota failure = %d, want 0", got)
	}

	st = loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, nil, quotaSink, time.Time{}); err != nil {
		t.Fatalf("quota retry: %v", err)
	}
	if quotaCalls != 2 {
		t.Fatalf("quota sink calls = %d, want 2", quotaCalls)
	}
	if got := st.Offset(logPath); got != 3 {
		t.Fatalf("offset after quota retry = %d, want 3", got)
	}
}

func TestScanSourcesRetriesQuotaWithoutReenqueuingDurableEvents(t *testing.T) {
	dir, logPath, statePath := writeScanFixture(t)
	quota := model.QuotaSnapshot{Device: "device", Source: "codex", LimitID: "weekly", ObservedAt: 123}
	spec := fixedScanSpec(dir, numberedEvents(1), []model.QuotaSnapshot{quota}, false)
	errQuota := errors.New("quota enqueue failed")
	eventCalls := 0
	quotaCalls := 0
	eventSink := func([]model.Event) error {
		eventCalls++
		return nil
	}
	quotaSink := func([]model.QuotaSnapshot) error {
		quotaCalls++
		if quotaCalls == 1 {
			return errQuota
		}
		return nil
	}

	st := loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, eventSink, quotaSink, time.Time{}); !errors.Is(err, errQuota) {
		t.Fatalf("first scan error = %v, want quota error", err)
	}
	st = loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, eventSink, quotaSink, time.Time{}); err != nil {
		t.Fatalf("quota retry: %v", err)
	}
	if eventCalls != 1 {
		t.Fatalf("event sink calls = %d, want 1", eventCalls)
	}
	if quotaCalls != 2 {
		t.Fatalf("quota sink calls = %d, want 2", quotaCalls)
	}
	if got := st.Offset(logPath); got != 3 {
		t.Fatalf("offset after quota retry = %d, want 3", got)
	}
}

func TestScanSourcesFullReparseFinishesFixedBoundaryBeforeAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollout.jsonl")
	statePath := filepath.Join(dir, "state.json")
	writeEvents := func(first, count int, flag int) {
		t.Helper()
		var data strings.Builder
		for i := first; i < first+count; i++ {
			fmt.Fprintf(&data, "event-%d\n", i)
		}
		file, err := os.OpenFile(logPath, flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(data.String()); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeEvents(0, 2001, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	oldEnd, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var parsedCounts []int
	spec := SourceSpec{
		Dirs:        []string{dir},
		FullReparse: true,
		Parse: func(r io.Reader, _ string, _ int64) model.ParseResult {
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(data))
			events := make([]model.Event, len(lines))
			for i, line := range lines {
				events[i] = model.Event{EventID: line}
			}
			parsedCounts = append(parsedCounts, len(events))
			return model.ParseResult{Events: events, Consumed: int64(len(data))}
		},
	}
	errFull := errors.New("one-batch capacity is full")
	occupied := false
	var accepted []string
	sink := func(events []model.Event) error {
		if occupied {
			return errFull
		}
		occupied = true
		accepted = append(accepted, events[0].EventID)
		return nil
	}

	st := loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); !errors.Is(err, errFull) {
		t.Fatalf("first scan error = %v, want capacity error", err)
	}
	writeEvents(2001, 2000, os.O_APPEND|os.O_WRONLY)

	occupied = false
	st = loadScanState(t, statePath)
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); err != nil {
		t.Fatalf("resume fixed boundary: %v", err)
	}
	if got := st.Offset(logPath); got != oldEnd.Size() {
		t.Fatalf("offset after resumed boundary = %d, want old end %d", got, oldEnd.Size())
	}
	if got := parsedCounts; fmt.Sprint(got) != "[2001 2001]" {
		t.Fatalf("parsed event counts = %v, want old 2001-event boundary twice", got)
	}
	if got := accepted; fmt.Sprint(got) != "[event-0 event-2000]" {
		t.Fatalf("accepted batch starts = %v, want old batches only", got)
	}

	occupied = false
	if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, func(events []model.Event) error {
		accepted = append(accepted, events[0].EventID)
		return nil
	}, nil, time.Time{}); err != nil {
		t.Fatalf("scan appended bytes: %v", err)
	}
	newEnd, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Offset(logPath); got != newEnd.Size() {
		t.Fatalf("final offset = %d, want new end %d", got, newEnd.Size())
	}
	if got := parsedCounts; fmt.Sprint(got) != "[2001 2001 4001]" {
		t.Fatalf("parsed event counts = %v, want full reparse after boundary commit", got)
	}
}

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

func TestLocalSpecsScanAntigravityAndGemini(t *testing.T) {
	root := t.TempDir()
	agyRoot := filepath.Join(root, "brain")
	agyLogs := filepath.Join(agyRoot, "conv-uuid-1", ".system_generated", "logs")
	if err := os.MkdirAll(filepath.Join(agyLogs, "chunks", "transcript"), 0o755); err != nil {
		t.Fatal(err)
	}
	agyLine := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-23T02:18:50Z","model":"gemini-4-neon-high","input_tokens":500,"cache_read_tokens":100,"output_tokens":50}` + "\n"
	if err := os.WriteFile(filepath.Join(agyLogs, "transcript.jsonl"), []byte(agyLine), 0o644); err != nil {
		t.Fatal(err)
	}
	// Duplicate copies that MatchFile must exclude.
	if err := os.WriteFile(filepath.Join(agyLogs, "transcript_full.jsonl"), []byte(agyLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agyLogs, "chunks", "transcript", "c0.jsonl"), []byte(agyLine), 0o644); err != nil {
		t.Fatal(err)
	}

	gemRoot := filepath.Join(root, "gemini-tmp")
	gemChats := filepath.Join(gemRoot, "proj-1", "chats")
	if err := os.MkdirAll(gemChats, 0o755); err != nil {
		t.Fatal(err)
	}
	gemSession := `{"sessionId":"gem-sess-1","startTime":"2026-11-23T02:20:00Z","messages":[{"id":"m1","timestamp":"2026-11-23T02:20:05Z","type":"gemini","model":"gemini-2.5-pro","tokens":{"input":300,"output":40,"cached":100,"thoughts":10,"total":350}}]}`
	if err := os.WriteFile(filepath.Join(gemChats, "session-1.json"), []byte(gemSession), 0o644); err != nil {
		t.Fatal(err)
	}

	specs := LocalSpecs(nil, nil, nil, []string{agyRoot}, []string{gemRoot}, nil)
	st := loadScanState(t, filepath.Join(root, "state.json"))
	var got []model.Event
	n, err := ScanSources(specs, "mac", st, nil, func(evs []model.Event) error {
		got = append(got, evs...)
		return nil
	}, nil, time.Time{})
	if err != nil {
		t.Fatalf("ScanSources: %v", err)
	}
	if n != 2 || len(got) != 2 {
		t.Fatalf("got %d events (%+v), want 2 (1 antigravity + 1 gemini)", len(got), got)
	}
	bySource := map[string]model.Event{}
	for _, ev := range got {
		bySource[ev.Source] = ev
	}
	if agy := bySource["antigravity"]; agy.SessionID != "conv-uuid-1" || agy.InputTokens != 500 || agy.CacheReadTokens != 100 {
		t.Errorf("antigravity event = %+v", agy)
	}
	if gem := bySource["gemini"]; gem.SessionID != "gem-sess-1" || gem.InputTokens != 200 || gem.CacheReadTokens != 100 || gem.OutputTokens != 50 {
		t.Errorf("gemini event = %+v", gem)
	}
}
