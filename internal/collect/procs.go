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
	args := splitArgs(command)
	if isServerMode(args) || isShellWrapper(args) {
		return ""
	}
	switch {
	case commandRuns(args, "claude"):
		return claudecode.Source
	case commandRuns(args, "codex"):
		return codex.Source
	}
	return ""
}

// splitArgs breaks a command line into argv tokens, honouring double quotes.
//
// Whitespace alone is enough on Unix, where ps prints an unquoted command. It
// is not enough on Windows, where the interesting paths live under
// `C:\Program Files\` and are therefore always quoted: splitting a quoted path
// on its space pushes the real second argument out of the two-token window
// commandRuns looks at, and the shim below stops being recognisable.
//
// Quotes are dropped rather than kept, so a token is the path itself and can be
// compared to a program name directly.
func splitArgs(command string) []string {
	var (
		args    []string
		current strings.Builder
		quoted  bool
	)
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return args
}

// shells are the interpreters that start a program without being one.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ash": true,
	"ksh": true, "fish": true, "cmd": true, "wscript": true, "cscript": true,
	"powershell": true, "pwsh": true,
}

// isShellWrapper rejects a shell that has an agent CLI's name on its command
// line, because the CLI it is about to run is a separate process that this scan
// picks up on its own.
//
// This is what Windows adds. Every npm-installed CLI there is launched through
// a shim — `sh.exe /c/Users/x/AppData/Roaming/npm/claude` — and MSYS sh stays
// alive as the parent of the claude.exe it spawns, so both are in the process
// table at once and a single session gets counted twice. Dropping the shim
// loses nothing: it was seen next to its own child on mypc, one second apart.
//
// The rule holds on Unix too and is not restricted to Windows, since the
// argument for it never mentions the platform: an open Claude Code session
// spawns a shell per tool call, and each one names `.claude` in a path.
func isShellWrapper(args []string) bool {
	return len(args) > 0 && shells[programName(args[0])]
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
func isServerMode(args []string) bool {
	for _, f := range args {
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
func commandRuns(args []string, name string) bool {
	if len(args) > 2 {
		args = args[:2]
	}
	for _, f := range args {
		if tokenIsBinary(f, name) {
			return true
		}
	}
	return false
}

// pathParts splits an argv token into path segments.
//
// Both separators are honoured on every platform rather than switching on
// runtime.GOOS: a Windows command line reaches this code through an agent's
// payload as readily as through the local scan, and MSYS hands out `/c/Users`
// paths on the very machine whose native ones use backslashes — the same shim
// line carries one of each.
func pathParts(token string) []string {
	return strings.FieldsFunc(token, func(r rune) bool { return r == '/' || r == '\\' })
}

// programName is the last path segment of an argv token — the file that would
// actually be executed.
func programName(token string) string {
	parts := pathParts(token)
	if len(parts) == 0 {
		return ""
	}
	base := parts[len(parts)-1]
	return strings.TrimSuffix(base, ".exe")
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
	parts := pathParts(token)
	if len(parts) == 0 {
		return false
	}
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
