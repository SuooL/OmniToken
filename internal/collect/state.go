package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// State tracks per-file read offsets and the cwd→repo cache so scans are
// incremental. It is persisted after every successful sink delivery, so a
// crash can only cause re-parsing (which ingestion dedup absorbs), never loss.
type State struct {
	mu        sync.Mutex
	path      string
	Files     map[string]int64  `json:"files"`       // abs path -> committed offset
	RepoByCWD map[string]string `json:"repo_by_cwd"` // cwd -> normalized repo ("" = probed, not a repo)
	// TurnStart carries each file's in-flight turn start across incremental
	// reads (ADR-0009). Written only by Commit, so it advances with the offset
	// and never ahead of it: a failed upload re-reads the same bytes and must
	// see the same turn start, or the retry would measure a different interval.
	TurnStart map[string]int64 `json:"turn_start,omitempty"`
}

func LoadState(path string) (*State, error) {
	st := &State{path: path, Files: map[string]int64{}, RepoByCWD: map[string]string{}, TurnStart: map[string]int64{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		// Corrupt state: start over; dedup makes re-import safe.
		return &State{path: path, Files: map[string]int64{}, RepoByCWD: map[string]string{}, TurnStart: map[string]int64{}}, nil
	}
	if st.Files == nil {
		st.Files = map[string]int64{}
	}
	if st.RepoByCWD == nil {
		st.RepoByCWD = map[string]string{}
	}
	if st.TurnStart == nil {
		st.TurnStart = map[string]int64{} // state written before ADR-0009
	}
	return st, nil
}

func (s *State) Offset(file string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Files[file]
}

// TurnStartFor returns the in-flight turn start carried over for a file.
func (s *State) TurnStartFor(file string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TurnStart[file]
}

// Commit advances a file's offset and its turn-start carry together. They must
// move as one: the carry describes the boundary the offset sits at, so storing
// one without the other would make the next read measure from the wrong place.
func (s *State) Commit(file string, offset, turnStartMS int64) error {
	s.mu.Lock()
	s.Files[file] = offset
	if turnStartMS > 0 {
		s.TurnStart[file] = turnStartMS
	}
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

// RepoFor caches per (device, cwd): different machines can have the same
// path pointing at different repos.
func (s *State) RepoFor(device, cwd string, resolve func(string) string) string {
	key := device + "|" + cwd
	s.mu.Lock()
	repo, ok := s.RepoByCWD[key]
	s.mu.Unlock()
	if ok {
		return repo
	}
	repo = resolve(cwd)
	s.mu.Lock()
	s.RepoByCWD[key] = repo
	s.saveLocked()
	s.mu.Unlock()
	return repo
}

func (s *State) saveLocked() error {
	data, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
