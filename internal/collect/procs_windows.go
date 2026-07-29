//go:build windows

package collect

// listProcs has no Windows implementation yet (ADR-0012).
//
// It returns empty rather than an error on purpose: the machine still collects
// events and quota perfectly well, and "no realtime session data" must not turn
// into a failed collection pass. Reading the Windows process table means
// Toolhelp32 through golang.org/x/sys — a real dependency, deferred until the
// Windows desktop client is scheduled.
func listProcs() ([]procInfo, error) { return nil, nil }
