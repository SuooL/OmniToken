package collect

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/parser/codex"
)

// SSHHost is a remote machine mirrored over the user's existing SSH access.
// Nothing needs to be installed remotely: rsync pulls the log dirs into a
// local mirror, which is then scanned like any local directory.
type SSHHost struct {
	Host string `json:"host"`           // ssh destination (alias from ~/.ssh/config works)
	Name string `json:"name,omitempty"` // device name; defaults to Host
}

func (h SSHHost) DeviceName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Host
}

// remotePullSets lists remote paths (relative to the remote home dir) per log
// format; each lands in mirror/<device>/<kind>-<i>/ so the right parser can
// be matched when scanning the mirror.
var remotePullSets = []struct {
	kind string
	dirs []string
}{
	{"claude", []string{".claude/projects", ".config/claude/projects"}},
	{"codex", []string{".codex/sessions", ".codex/archived_sessions"}},
}

// SyncSSHHost mirrors the remote log dirs under mirrorRoot/<name>/.
// Only *.jsonl files are transferred; deletions are NOT propagated, so data
// survives locally even after remote log cleanup (cleanupPeriodDays).
func SyncSSHHost(h SSHHost, mirrorRoot string) (string, error) {
	dest := filepath.Join(mirrorRoot, h.DeviceName())
	var lastErr error
	synced := 0
	for _, set := range remotePullSets {
		for i, rd := range set.dirs {
			local := filepath.Join(dest, fmt.Sprintf("%s-%d", set.kind, i))
			cmd := exec.Command("rsync",
				"-az", "--timeout=30",
				"-e", "ssh -o BatchMode=yes -o ConnectTimeout=10",
				"--include=*/", "--include=*.jsonl", "--exclude=*",
				"--prune-empty-dirs",
				fmt.Sprintf("%s:%s/", h.Host, rd),
				local+"/",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				msg := strings.TrimSpace(string(out))
				// A missing remote dir is normal (e.g. no ~/.codex at all).
				if strings.Contains(msg, "No such file or directory") || strings.Contains(msg, "change_dir") {
					continue
				}
				lastErr = fmt.Errorf("rsync %s:%s: %v: %s", h.Host, rd, err, truncate(msg, 200))
				continue
			}
			synced++
		}
	}
	if synced == 0 && lastErr != nil {
		return dest, lastErr
	}
	return dest, nil
}

// MirrorSpecs maps a synced mirror directory back to parser specs.
func MirrorSpecs(mirrorDest string) []SourceSpec {
	claude := []string{filepath.Join(mirrorDest, "claude-0"), filepath.Join(mirrorDest, "claude-1")}
	cx := []string{filepath.Join(mirrorDest, "codex-0"), filepath.Join(mirrorDest, "codex-1")}
	return []SourceSpec{
		{Dirs: claude, Parse: claudecode.Parse},
		{Dirs: cx, Parse: codex.Parse, FullReparse: true},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
