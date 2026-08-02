//go:build !linux && !windows

package collect

import (
	"os/exec"
	"time"
)

// listProcs shells out to ps on macOS and the BSDs, the same choice abtop made.
//
// This does not break N1 (single binary, no CGO, cross-compilable): ps ships
// with every Unix, whereas a cross-platform process library pulls in a large
// dependency tree to read a handful of fields.
//
// lstart is asked for instead of the elapsed-time column because it is an
// absolute instant — a start time stays correct in a payload that may sit in a
// retry buffer, while "running for 3h" silently ages.
func listProcs() ([]procInfo, error) {
	out, err := exec.Command("ps", "-axo", "pid,lstart,command").Output()
	if err != nil {
		return nil, err
	}
	// ps prints lstart in the machine's local zone with no offset, so it can
	// only be read back as local time.
	return parsePS(string(out), time.Local), nil
}
