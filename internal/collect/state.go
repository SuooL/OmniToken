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
}

func LoadState(path string) (*State, error) {
	st := &State{path: path, Files: map[string]int64{}, RepoByCWD: map[string]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		// Corrupt state: start over; dedup makes re-import safe.
		return &State{path: path, Files: map[string]int64{}, RepoByCWD: map[string]string{}}, nil
	}
	if st.Files == nil {
		st.Files = map[string]int64{}
	}
	if st.RepoByCWD == nil {
		st.RepoByCWD = map[string]string{}
	}
	return st, nil
}

func (s *State) Offset(file string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Files[file]
}

func (s *State) Commit(file string, offset int64) error {
	s.mu.Lock()
	s.Files[file] = offset
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
