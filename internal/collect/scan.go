package collect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/parser/codex"
)

const sinkBatch = 2000

// Sink delivers a batch of events durably (DB insert or HTTP push).
// A file's offset is only committed after its events are accepted.
type Sink func([]model.Event) error

// QuotaSink delivers quota snapshots (ADR-0007). A non-nil sink is part of the
// source-cursor durability boundary: its error leaves the offset uncommitted.
// A nil sink explicitly opts out of collecting snapshots.
type QuotaSink func([]model.QuotaSnapshot) error

// ParseFunc is the incremental-parser contract shared by all tool parsers:
// consume complete lines from r and return the events, quota snapshots, and
// the byte count consumed (excluding any trailing partial line).
type ParseFunc func(r io.Reader, device string, turnStartMS int64) model.ParseResult

// SourceSpec pairs a set of log directories with the parser for their format.
// FullReparse marks formats whose lines are not self-contained (Codex rollout
// files carry model/cwd context in header lines): on any growth the whole
// file is re-parsed — event IDs are deterministic, so ingestion dedup makes
// that free of double counting.
type SourceSpec struct {
	Dirs        []string
	Parse       ParseFunc
	FullReparse bool
}

// LocalSpecs builds the scan specs for logs on this machine.
func LocalSpecs(claudeDirs, codexDirs []string) []SourceSpec {
	return []SourceSpec{
		{Dirs: claudeDirs, Parse: claudecode.Parse},
		{Dirs: codexDirs, Parse: codex.Parse, FullReparse: true},
	}
}

// ScanSources incrementally parses all *.jsonl under each spec's dirs,
// attributing events to device and resolving repos via resolveRepo.
// quotaSink may be nil.
//
// notBefore is the start of collection for this source: events with an earlier
// timestamp are dropped instead of being handed to the sink, and an event
// exactly at that instant is kept. The zero time means no window. It exists for
// SSH hosts (SSHHost.Since) — adding a machine years after the fact should not
// back-import its whole log history — and is left zero for this machine's own
// logs, which are the one set of logs we can attribute with confidence.
func ScanSources(specs []SourceSpec, device string, st *State, resolveRepo func(string) string, sink Sink, quotaSink QuotaSink, notBefore time.Time) (int, error) {
	total := 0
	for _, spec := range specs {
		for _, f := range listJSONL(spec.Dirs) {
			n, err := scanFile(f, spec, device, st, resolveRepo, sink, quotaSink, notBefore)
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// dropBefore removes events older than notBefore. The boundary instant itself
// is kept: a window that starts on a date should contain everything from that
// date's first millisecond on.
func dropBefore(events []model.Event, notBefore time.Time) []model.Event {
	if notBefore.IsZero() {
		return events
	}
	cutoff := notBefore.UnixMilli()
	kept := events[:0]
	for _, e := range events {
		if e.TS >= cutoff {
			kept = append(kept, e)
		}
	}
	return kept
}

func listJSONL(dirs []string) []string {
	var files []string
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable subtrees
			}
			if !d.IsDir() && filepath.Ext(path) == ".jsonl" {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

func scanFile(path string, spec SourceSpec, device string, st *State, resolveRepo func(string) string, sink Sink, quotaSink QuotaSink, notBefore time.Time) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	offset := st.Offset(path)
	if info.Size() < offset {
		// File truncated/replaced (e.g. rsync rewrote it): re-read; dedup absorbs repeats.
		offset = 0
		if err := st.DiscardScan(path); err != nil {
			return 0, fmt.Errorf("discard scan state for truncated %s: %w", path, err)
		}
	}

	inFlight, resuming := st.InFlightFor(path)
	if resuming && info.Size() < inFlight.End {
		// The fixed input boundary no longer exists. Forget only the in-flight
		// delivery ledger and safely re-read; durable ingestion deduplicates it.
		if err := st.DiscardScan(path); err != nil {
			return 0, fmt.Errorf("discard scan state for replaced %s: %w", path, err)
		}
		resuming = false
	}
	if !resuming && info.Size() == offset {
		return 0, nil
	}

	start, end := offset, info.Size()
	if resuming {
		start, end = inFlight.Start, inFlight.End
	} else if spec.FullReparse {
		start = 0
	}
	fh, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer fh.Close()
	if _, err := fh.Seek(start, 0); err != nil {
		return 0, nil
	}

	res := spec.Parse(io.LimitReader(fh, end-start), device, st.TurnStartFor(path))
	if !resuming {
		end = start + res.Consumed
		if err := st.BeginScan(path, start, end, res.TurnStartMS); err != nil {
			return 0, fmt.Errorf("begin scan state for %s: %w", path, err)
		}
		inFlight, _ = st.InFlightFor(path)
	} else if start+res.Consumed != end {
		return 0, fmt.Errorf(
			"resume scan state for %s: parser consumed %d bytes, want %d",
			path, res.Consumed, end-start,
		)
	}
	events := dropBefore(res.Events, notBefore)
	for i := range events {
		if events[i].CWD != "" && resolveRepo != nil {
			events[i].Repo = st.RepoFor(device, events[i].CWD, resolveRepo)
		}
	}
	for start := 0; start < len(events); start += sinkBatch {
		end := min(start+sinkBatch, len(events))
		key, err := logicalDeliveryKey("events", start/sinkBatch, events[start:end])
		if err != nil {
			return 0, fmt.Errorf("identify event batch for %s: %w", path, err)
		}
		if st.DeliveryDone(path, key) {
			continue
		}
		if err := sink(events[start:end]); err != nil {
			return 0, err // do not commit offset; retry next scan
		}
		if err := st.MarkDeliveryDone(path, key); err != nil {
			return 0, fmt.Errorf("persist event delivery for %s: %w", path, err)
		}
	}
	if quotaSink != nil && len(res.Quotas) > 0 {
		key, err := logicalDeliveryKey("quotas", 0, res.Quotas)
		if err != nil {
			return 0, fmt.Errorf("identify quota batch for %s: %w", path, err)
		}
		if !st.DeliveryDone(path, key) {
			if err := quotaSink(res.Quotas); err != nil {
				return 0, fmt.Errorf("quota sink for %s: %w", path, err)
			}
			if err := st.MarkDeliveryDone(path, key); err != nil {
				return 0, fmt.Errorf("persist quota delivery for %s: %w", path, err)
			}
		}
	}

	// The offset covers the dropped lines as well: they were read and
	// deliberately left out of the window, not deferred to a later pass.
	if err := st.Commit(path, end, inFlight.TurnStartMS); err != nil {
		return 0, fmt.Errorf("commit state for %s: %w", path, err)
	}
	return len(events), nil
}

func logicalDeliveryKey(kind string, ordinal int, payload any) (string, error) {
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%s:%d:%x", kind, ordinal, sum), nil
}
