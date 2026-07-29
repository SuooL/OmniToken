//go:build linux

package collect

import (
	"os"
	"strconv"
	"strings"
)

// listProcs reads /proc directly — no shelling out, no external dependency.
func listProcs() ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, 32)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // /proc holds plenty of non-PID entries
		}
		dir := "/proc/" + e.Name()
		raw, err := os.ReadFile(dir + "/cmdline")
		if err != nil || len(raw) == 0 {
			continue // exited between ReadDir and now, or a kernel thread
		}
		// Start time comes from the directory's mtime, which the kernel sets
		// when the process is created. The alternative — field 22 of
		// /proc/<pid>/stat — is in clock ticks since boot and needs sysconf
		// (CGO) plus btime to convert, for the same answer.
		var startedAt int64
		if fi, err := os.Stat(dir); err == nil {
			startedAt = fi.ModTime().UnixMilli()
		}
		out = append(out, procInfo{
			pid:       pid,
			startedAt: startedAt,
			command:   strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " ")),
		})
	}
	return out, nil
}
