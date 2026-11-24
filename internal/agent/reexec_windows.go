//go:build windows

package agent

import "os"

// reexecSelfProcess exits with EX_TEMPFAIL (75) on Windows so the host
// supervisor (Task Scheduler RestartOnFailure or Windows Service recovery)
// launches the freshly staged omnitoken.exe.
func reexecSelfProcess(_ string) error {
	os.Exit(75)
	return nil
}
