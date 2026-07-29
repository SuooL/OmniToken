package collect

import (
	"testing"
	"time"
)

// Real `ps -axo pid,lstart,command` lines from a development Mac, trimmed but
// not rewritten. The noise is the point: every line here except the two agent
// CLIs sits close enough to one to be a plausible false positive.
const psSample = `  PID STARTED                      COMMAND
    1 Wed Jul 22 20:53:25 2026     /sbin/launchd
11941 Wed Jul 29 05:12:03 2026     claude
32011 Wed Jul 29 05:40:11 2026     /Users/x/.local/bin/codex exec --full-auto
26924 Tue Jul 28 22:59:22 2026     /Users/x/.vscode/extensions/openai.chatgpt-26.721.41059-darwin-arm64/bin/macos-aarch64/codex -c features.code_mode_host=true app-server --analytics-default-enabled
52854 Wed Jul 29 03:38:41 2026     /Applications/ChatGPT.app/Contents/Resources/codex -c features.code_mode_host=true app-server --analytics-default-enabled
52674 Wed Jul 29 03:38:39 2026     /Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/150.0.7871.128/Helpers/Codex (Service).app/Contents/MacOS/Codex (Service) --type=gpu-process
48539 Tue Jul 28 23:18:13 2026     /Applications/Claude Science.app/Contents/MacOS/ClaudeScience
51280 Tue Jul 28 23:20:08 2026     /Users/x/.claude-science/bin/claude-science serve --app --port 8765
63346 Tue Jul 28 23:31:02 2026     /Applications/Claude.app/Contents/MacOS/Claude
53116 Wed Jul 29 05:55:00 2026     /usr/bin/ssh -T -v -o BatchMode=yes macmini sh -c 'PATH="$HOME/.local/bin:$PATH"; codex app-server proxy'
16637 Wed Jul 29 06:00:30 2026     ugrep -G --hidden -i claude\|codex
16630 Wed Jul 29 06:00:30 2026     /bin/zsh -c eval 'ps -axo pid,lstart,command' && echo claude
 9001 Wed Jul 29 05:59:00 2026     omnitoken agent -config /etc/omnitoken/agent.json`

func TestParsePSKeepsOnlyAgentCLIs(t *testing.T) {
	procs := parsePS(psSample, time.UTC)
	if len(procs) != 13 {
		t.Fatalf("parsePS: got %d records, want 13 (header must be skipped)", len(procs))
	}
	// PID 1 is not our process; a self PID that matches nothing keeps the test
	// independent of the machine it runs on.
	got := agentSessions(procs, 1)
	if len(got) != 2 {
		for _, s := range got {
			t.Logf("matched pid=%d source=%s", s.PID, s.Source)
		}
		t.Fatalf("agentSessions: got %d, want 2 (claude + codex exec)", len(got))
	}
	// Oldest first.
	if got[0].PID != 11941 || got[0].Source != "claude-code" {
		t.Errorf("first session = %+v, want pid 11941 claude-code", got[0])
	}
	if got[1].PID != 32011 || got[1].Source != "codex" {
		t.Errorf("second session = %+v, want pid 32011 codex", got[1])
	}
}

// Server-mode processes are the same binary, so only the subcommand tells them
// apart from a session. They outlive every session on the machine, so counting
// them would pin the panel to "codex is running" forever. The mcp-server pair
// is worse than useless: those two are children of a *Claude* session, seen on
// this machine while writing this test.
func TestServerModeProcessesAreNotSessions(t *testing.T) {
	for _, cmd := range []string{
		"/Users/x/.vscode/extensions/openai.chatgpt/bin/codex -c features.code_mode_host=true app-server --analytics-default-enabled",
		"/Applications/ChatGPT.app/Contents/Resources/codex app-server",
		"node /opt/homebrew/bin/codex mcp-server",
		"/opt/homebrew/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex mcp-server",
		"claude mcp list",
	} {
		if tool := toolFor(cmd); tool != "" {
			t.Errorf("toolFor(%q) = %q, want no match", cmd, tool)
		}
	}
	if tool := toolFor("/Users/x/.local/bin/codex exec --full-auto"); tool != "codex" {
		t.Errorf("interactive codex was filtered out: got %q", tool)
	}
}

func TestParsePSReadsStartTime(t *testing.T) {
	procs := parsePS("    1 Wed Jul 22 20:53:25 2026     /sbin/launchd", time.UTC)
	if len(procs) != 1 {
		t.Fatalf("got %d records, want 1", len(procs))
	}
	want := time.Date(2026, 7, 22, 20, 53, 25, 0, time.UTC).UnixMilli()
	if procs[0].startedAt != want {
		t.Errorf("startedAt = %d, want %d", procs[0].startedAt, want)
	}
}

// A process whose start time cannot be read is still a running session; losing
// the timestamp must not lose the process.
func TestParsePSKeepsProcessWithUnreadableStartTime(t *testing.T) {
	procs := parsePS("42 not a real date here /usr/bin/claude", time.UTC)
	if len(procs) != 1 || procs[0].pid != 42 {
		t.Fatalf("got %+v, want one record for pid 42", procs)
	}
	if procs[0].startedAt != 0 {
		t.Errorf("startedAt = %d, want 0", procs[0].startedAt)
	}
	if toolFor(procs[0].command) != "claude-code" {
		t.Errorf("command %q did not match claude", procs[0].command)
	}
}

func TestAgentSessionsSkipsSelf(t *testing.T) {
	procs := []procInfo{{pid: 99, startedAt: 1, command: "/usr/bin/claude"}}
	if got := agentSessions(procs, 99); len(got) != 0 {
		t.Errorf("agentSessions included our own pid: %+v", got)
	}
	if got := agentSessions(procs, 98); len(got) != 1 {
		t.Errorf("agentSessions dropped a real session: %+v", got)
	}
}

func TestToolForRejectsLookalikes(t *testing.T) {
	// Left column must not match: same prefix, different program.
	for _, cmd := range []string{
		"/Applications/Claude Science.app/Contents/MacOS/ClaudeScience",
		"/Users/x/.claude-science/bin/claude-science serve",
		"/usr/local/bin/claude-monitor --watch",
		"/opt/homebrew/bin/codexctl status",
		"vim /tmp/claude.md",
	} {
		if tool := toolFor(cmd); tool != "" {
			t.Errorf("toolFor(%q) = %q, want no match", cmd, tool)
		}
	}
	for cmd, want := range map[string]string{
		"claude":                                        "claude-code",
		"/usr/local/bin/claude --resume":                "claude-code",
		"/Users/x/.local/bin/claude.exe":                "claude-code",
		"/Users/x/.local/share/claude/versions/2.1.121": "claude-code",
		"node /usr/lib/node_modules/codex/bin/codex.js": "codex",
		"/opt/homebrew/bin/codex exec":                  "codex",
	} {
		if tool := toolFor(cmd); tool != want {
			t.Errorf("toolFor(%q) = %q, want %q", cmd, tool, want)
		}
	}
}
