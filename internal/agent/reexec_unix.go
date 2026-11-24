//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// reexecSelfProcess replaces the current process image in-place with the newly
// installed binary, preserving PID, arguments, environment, and supervisor
// stdio descriptors (systemd / launchd).
func reexecSelfProcess(binaryPath string) error {
	return syscall.Exec(binaryPath, os.Args, os.Environ())
}
