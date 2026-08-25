package collect

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// lineParse is a FullReparse spec whose parser turns each whitespace-separated
// token into an event with that EventID — enough to exercise the delivery layer
// without a real log format.
func lineParse(t *testing.T, dir string) SourceSpec {
	t.Helper()
	return SourceSpec{
		Dirs:        []string{dir},
		FullReparse: true,
		Parse: func(r io.Reader, _ string, _ int64) model.ParseResult {
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			toks := strings.Fields(string(data))
			evs := make([]model.Event, len(toks))
			for i, tok := range toks {
				evs[i] = model.Event{EventID: tok}
			}
			return model.ParseResult{Events: evs, Consumed: int64(len(data))}
		},
	}
}

func appendTokens(t *testing.T, path string, first, count int, flag int) {
	t.Helper()
	var b strings.Builder
	for i := first; i < first+count; i++ {
		fmt.Fprintf(&b, "event-%d\n", i)
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestFullReparseSkipsUnchangedChunksAcrossScans is the core of the cross-scan
// dedup (ADR-0032): a growing FullReparse file re-reads from byte zero every
// scan, but a chunk whose content has not changed since it was last delivered
// must not be sent to the sink again. Only the frontier chunk (which grew) and
// any chunk whose events changed should be re-delivered.
func TestFullReparseSkipsUnchangedChunksAcrossScans(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollout.jsonl")
	statePath := filepath.Join(dir, "state.json")
	spec := lineParse(t, dir)

	var delivered [][]string
	sink := func(events []model.Event) error {
		ids := make([]string, len(events))
		for i, e := range events {
			ids[i] = e.EventID
		}
		delivered = append(delivered, ids)
		return nil
	}
	scan := func() {
		st := loadScanState(t, statePath)
		if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}

	// chunk 0 = [0:2000] full, chunk 1 = [2000:2500] partial.
	appendTokens(t, logPath, 0, 2500, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	scan()
	if len(delivered) != 2 || delivered[0][0] != "event-0" || delivered[1][0] != "event-2000" {
		t.Fatalf("first scan delivered %d batches starting %v, want [event-0 event-2000]",
			len(delivered), batchStarts(delivered))
	}
	delivered = nil

	// Append 100 events: chunk 0 is byte-for-byte unchanged, chunk 1 grows.
	appendTokens(t, logPath, 2500, 100, os.O_APPEND|os.O_WRONLY)
	scan()
	if len(delivered) != 1 {
		t.Fatalf("second scan delivered %d batches (%v), want 1 — the unchanged chunk 0 must be skipped",
			len(delivered), batchStarts(delivered))
	}
	if delivered[0][0] != "event-2000" || len(delivered[0]) != 600 {
		t.Fatalf("second scan re-delivered %d events starting %s, want the 600-event chunk 1 from event-2000",
			len(delivered[0]), delivered[0][0])
	}

	// Nothing appended: the whole file is unchanged, so nothing is re-delivered.
	delivered = nil
	scan()
	if len(delivered) != 0 {
		t.Fatalf("no-growth scan delivered %d batches (%v), want 0", len(delivered), batchStarts(delivered))
	}
}

// TestFullReparseRedeliversChunkWhoseEventsChanged guards the case that makes
// cross-scan dedup content-keyed rather than byte-keyed: when a turn closes,
// closeTurn writes gen_ms back over already-delivered events (ADR-0009), so the
// same bytes now parse to different events. That chunk MUST re-deliver, or the
// gen_ms fill never reaches the store. Here the parser stamps a field onto
// chunk 0's events on the second pass while the bytes are untouched.
func TestFullReparseRedeliversChunkWhoseEventsChanged(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollout.jsonl")
	statePath := filepath.Join(dir, "state.json")

	backfill := false
	spec := SourceSpec{
		Dirs:        []string{dir},
		FullReparse: true,
		Parse: func(r io.Reader, _ string, _ int64) model.ParseResult {
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			toks := strings.Fields(string(data))
			evs := make([]model.Event, len(toks))
			for i, tok := range toks {
				evs[i] = model.Event{EventID: tok}
				if backfill && i < 2000 { // chunk 0's events gain a measured interval
					evs[i].GenMS = 5000
				}
			}
			return model.ParseResult{Events: evs, Consumed: int64(len(data))}
		},
	}

	var delivered [][]string
	sink := func(events []model.Event) error {
		ids := make([]string, len(events))
		for i, e := range events {
			ids[i] = e.EventID
		}
		delivered = append(delivered, ids)
		return nil
	}
	scan := func() {
		st := loadScanState(t, statePath)
		if _, err := ScanSources([]SourceSpec{spec}, "device", st, nil, sink, nil, time.Time{}); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}

	appendTokens(t, logPath, 0, 2500, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	scan()
	delivered = nil

	// The turn closes: chunk 0's events gain gen_ms, and the file also grows.
	backfill = true
	appendTokens(t, logPath, 2500, 100, os.O_APPEND|os.O_WRONLY)
	scan()

	if got := batchStarts(delivered); len(got) != 2 || got[0] != "event-0" || got[1] != "event-2000" {
		t.Fatalf("re-delivered %v, want both chunks [event-0 event-2000] — chunk 0 changed, chunk 1 grew", got)
	}
}

func batchStarts(batches [][]string) []string {
	out := make([]string, len(batches))
	for i, b := range batches {
		if len(b) > 0 {
			out[i] = b[0]
		}
	}
	return out
}
