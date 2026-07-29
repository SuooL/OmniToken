// Live agent processes (ADR-0012).
//
// The panel's "active sessions" is inferred from recent events, which cannot
// tell a session that is open but idle from one that closed three minutes ago.
// Reading the local process table answers that directly. This is the project's
// only platform-specific code; see the build-tagged files beside this one.

package collect

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/parser/codex"
)

// procInfo is what every platform implementation has to produce.
type procInfo struct {
	pid       int
	startedAt int64 // unix ms; 0 when the platform does not report it
	command   string
}

// LiveProcesses reports the agent CLIs running on this machine right now.
//
// The returned report is always complete for the device, including when no
// agent is running: an empty Sessions list is the statement "nothing is
// running here", which is different from never reporting at all. Callers must
// send it either way, or the panel cannot tell an idle machine from one with
// no agent installed.
//
// On a platform with no implementation (Windows) this returns an empty report
// and no error — missing realtime data must not fail the whole collection pass.
func LiveProcesses(device string, now time.Time) (model.ProcReport, error) {
	report := model.ProcReport{Device: device, ObservedAt: now.UnixMilli()}
	procs, err := listProcs()
	if err != nil {
		return report, err
	}
	report.Sessions = agentSessions(procs, os.Getpid())
	return report, nil
}

// agentSessions keeps the agent CLIs out of a raw process list. Split out from
// LiveProcesses so the matching rules can be tested without a process table.
func agentSessions(procs []procInfo, selfPID int) []model.ProcSession {
	var out []model.ProcSession
	for _, p := range procs {
		if isSelfOrHelper(p.command, selfPID, p.pid) {
			continue
		}
		tool := toolFor(p.command)
		if tool == "" {
			continue
		}
		out = append(out, model.ProcSession{PID: p.pid, Source: tool, StartedAt: p.startedAt})
	}
	// Oldest first: a long-lived session is the interesting one, and a stable
	// order keeps the panel from reshuffling rows between polls.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// psLayout matches ps(1)'s lstart column: "Wed Jul 29 05:49:25 2026".
// Fields are re-joined with single spaces before parsing, which is why the day
// carries no padding here.
const psLayout = "Mon Jan 2 15:04:05 2006"

// parsePS turns `ps -axo pid,lstart,command` output into process records.
//
// It lives in the shared file rather than beside the exec call so it is tested
// on every platform, not only the ones that shell out to ps.
//
// Unparseable lines are skipped rather than failing the batch: ps prints a
// header, and a process can exit while it is being listed.
func parsePS(out string, loc *time.Location) []procInfo {
	var procs []procInfo
	for _, line := range strings.Split(out, "\n") {
		// pid + five lstart fields + at least one command token.
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		var startedAt int64
		if t, err := time.ParseInLocation(psLayout, strings.Join(fields[1:6], " "), loc); err == nil {
			startedAt = t.UnixMilli()
		}
		procs = append(procs, procInfo{
			pid:       pid,
			startedAt: startedAt,
			command:   strings.Join(fields[6:], " "),
		})
	}
	return procs
}

// toolFor maps a command line to a source name, or "" if it is not an agent CLI.
func toolFor(command string) string {
	if isServerMode(command) {
		return ""
	}
	switch {
	case commandRuns(command, "claude"):
		return claudecode.Source
	case commandRuns(command, "codex"):
		return codex.Source
	}
	return ""
}

// serverSubcommands are the modes in which an agent CLI is a background server
// waiting for RPC, not a session someone is using.
var serverSubcommands = map[string]bool{
	"app-server": true, // IDE extension backend
	"mcp-server": true, // codex as an MCP server
	"mcp":        true, // `claude mcp ...`, and codex's older spelling
}

// isServerMode rejects the right binary answering the wrong question.
//
// Both cases turned up on a real process table rather than by reasoning:
//   - the ChatGPT desktop app and the VS Code extension each keep a
//     `codex ... app-server` alive for as long as the editor is open;
//   - a Claude Code session that has the Codex plugin loaded spawns
//     `node .../codex mcp-server` plus its native child, so one *Claude*
//     session would be counted as two Codex ones.
//
// Either would pin a permanent "codex is running" to the panel — precisely the
// uninformative signal this feature exists to replace. An interactive session
// (`codex`, `codex exec`, `claude`) still counts.
func isServerMode(command string) bool {
	for _, f := range strings.Fields(command) {
		if serverSubcommands[f] {
			return true
		}
	}
	return false
}

// commandRuns reports whether a command line invokes the named binary.
//
// Only the first two arguments are considered, which is what makes
// `node /path/to/codex.js` match while leaving a stray mention of the name in a
// later flag alone.
func commandRuns(command, name string) bool {
	fields := strings.Fields(command)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	for _, f := range fields {
		if tokenIsBinary(f, name) {
			return true
		}
	}
	return false
}

// tokenIsBinary matches one argv token against a binary name.
//
// Three of the cases here are not obvious:
//   - the @anthropic-ai/claude-code npm package ships a file called claude.exe
//     even on macOS, so the suffix is stripped on every platform (abtop hit
//     this in the wild);
//   - an npm install can be launched through its shim, which shows up as
//     `node <...>/codex.js`, so .js is stripped for the same reason;
//   - Claude Code 2.x's autoupdater lays binaries out as
//     <...>/claude/versions/2.1.121, so the file name is a version string and
//     the directory above it carries the name.
//
// Matching is case-sensitive, and deliberately so: ChatGPT.app's helper
// processes live under a `Codex Framework.framework` path whose last segment is
// `Codex`, and the capital is the only thing separating them from the CLI.
func tokenIsBinary(token, name string) bool {
	parts := strings.Split(token, "/")
	base := parts[len(parts)-1]
	if base == name ||
		strings.TrimSuffix(base, ".exe") == name ||
		strings.TrimSuffix(base, ".js") == name {
		return true
	}
	return len(parts) >= 3 &&
		parts[len(parts)-2] == "versions" &&
		parts[len(parts)-3] == name
}

// isSelfOrHelper filters out this process and the shell plumbing that mentions
// an agent's name without being one — a grep for it, or our own invocation.
func isSelfOrHelper(command string, selfPID, pid int) bool {
	if pid == selfPID {
		return true
	}
	return strings.Contains(command, "grep ") || strings.Contains(command, "omnitoken ")
}
