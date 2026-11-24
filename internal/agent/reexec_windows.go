//go:build windows

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// reexecSelfProcess spawns the freshly installed omnitoken.exe directly with
// the same arguments and environment, then exits 0. Windows Task Scheduler's
// RestartOnFailure only retries launch errors (not non-zero process exit
// codes), so spawning the replacement directly ensures seamless auto-restart
// under Scheduled Tasks, Startup shortcuts, and manual invocations alike.
func reexecSelfProcess(binaryPath string) error {
	cmd := exec.Command(binaryPath, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
