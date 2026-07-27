package collect

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/parser/codex"
)

const sinkBatch = 2000

// Sink delivers a batch of events durably (DB insert or HTTP push).
// A file's offset is only committed after its events are accepted.
type Sink func([]model.Event) error

// QuotaSink delivers quota snapshots (ADR-0007). Optional: a nil sink means
// snapshots are dropped, which is safe — they are state, not flow.
type QuotaSink func([]model.QuotaSnapshot) error

// ParseFunc is the incremental-parser contract shared by all tool parsers:
// consume complete lines from r and return the events, quota snapshots, and
// the byte count consumed (excluding any trailing partial line).
type ParseFunc func(r io.Reader, device string) model.ParseResult

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
func ScanSources(specs []SourceSpec, device string, st *State, resolveRepo func(string) string, sink Sink, quotaSink QuotaSink) (int, error) {
	total := 0
	for _, spec := range specs {
		for _, f := range listJSONL(spec.Dirs) {
			n, err := scanFile(f, spec, device, st, resolveRepo, sink, quotaSink)
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
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

func scanFile(path string, spec SourceSpec, device string, st *State, resolveRepo func(string) string, sink Sink, quotaSink QuotaSink) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	offset := st.Offset(path)
	if info.Size() < offset {
		// File truncated/replaced (e.g. rsync rewrote it): re-read; dedup absorbs repeats.
		offset = 0
	}
	if info.Size() == offset {
		return 0, nil
	}
	if spec.FullReparse {
		offset = 0
	}
	fh, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, 0); err != nil {
		return 0, nil
	}

	res := spec.Parse(fh, device)
	events := res.Events
	for i := range events {
		if events[i].CWD != "" && resolveRepo != nil {
			events[i].Repo = st.RepoFor(device, events[i].CWD, resolveRepo)
		}
	}
	for start := 0; start < len(events); start += sinkBatch {
		end := min(start+sinkBatch, len(events))
		if err := sink(events[start:end]); err != nil {
			return 0, err // do not commit offset; retry next scan
		}
	}
	// Quota snapshots are state, not flow: a delivery failure must not hold
	// back the offset (the next observation supersedes a lost one anyway).
	if quotaSink != nil && len(res.Quotas) > 0 {
		if err := quotaSink(res.Quotas); err != nil {
			log.Printf("collect: quota sink for %s: %v", path, err)
		}
	}
	if err := st.Commit(path, offset+res.Consumed); err != nil {
		log.Printf("collect: commit state for %s: %v", path, err)
	}
	return len(events), nil
}
