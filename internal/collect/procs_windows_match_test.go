package collect

import "testing"

// Real `Get-CimInstance Win32_Process` command lines from mypc (Windows 10,
// Git Bash, npm-installed CLIs), captured while one Claude Code session was
// open in a terminal and another ran under the happy daemon.
//
// This sample exists separately from psSample because Windows breaks two
// assumptions the Unix one never tests: paths are separated by `\`, and the
// npm-installed CLIs are launched through a shell shim that appears in the
// process table right next to the binary it starts.
var windowsSample = []procInfo{
	// The two genuine sessions. Both are native binaries whose path uses
	// backslashes throughout — the only tokens in this list that name the CLI
	// itself rather than something starting it.
	{pid: 632160, startedAt: 1785549309000, command: `C:\Users\hu\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`},
	{pid: 637988, startedAt: 1785544398000, command: `C:\Users\hu\AppData\Roaming\npm\node_modules\happy\node_modules\@anthropic-ai\claude-agent-sdk-win32-x64\claude.exe --output-format stream-json --verbose --input-format stream-json --effort medium`},

	// The npm shim for the session above: MSYS sh runs the shell script that
	// spawns claude.exe, and stays alive as its parent. Counting it doubles
	// every npm-installed session.
	{pid: 641232, startedAt: 1785549309000, command: `"C:\Program Files\Git\usr\bin\sh.exe" /c/Users/hu/AppData/Roaming/npm/claude`},

	// Codex in server mode, in all four forms it takes on this machine: the
	// shim, the node entry point, the vendored native binary, and the one
	// inside the ChatGPT desktop app. ADR-0012 keeps every one of them out.
	{pid: 644020, startedAt: 1785551136000, command: `"C:\Program Files\Git\usr\bin\sh.exe" /c/Users/hu/AppData/Roaming/npm/codex app-server proxy`},
	{pid: 630912, startedAt: 1785551137000, command: `"C:\Program Files\nodejs\node.exe" C:\Users\hu\AppData\Roaming\npm/node_modules/@openai/codex/bin/codex.js app-server proxy`},
	{pid: 410752, startedAt: 1785551137000, command: `C:\Users\hu\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe app-server proxy`},
	{pid: 513996, startedAt: 1785499208000, command: `"C:\Program Files\WindowsApps\OpenAI.Codex_26.721.4979.0_x64__2p2nqsd0c76g0\app\resources\codex.exe" -c features.code_mode_host=true app-server --analytics-default-enabled`},

	// The ChatGPT desktop app itself. `OpenAI.Codex_26.721...` is a path
	// segment, not the program — matching it would pin "codex is running" to
	// the panel for as long as the app is installed and open.
	{pid: 637164, startedAt: 1785499144000, command: `"C:\Program Files\WindowsApps\OpenAI.Codex_26.721.4979.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe" `},
	{pid: 639112, startedAt: 1785499146000, command: `"C:\Program Files\WindowsApps\OpenAI.Codex_26.721.4979.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe" --type=utility --utility-sub-type=network.mojom.NetworkService --lang=zh-CN --service-sandbox-type=none --standard-schemes=app,codex-sandbox`},

	// Codex's sandbox helper: the file name starts with `codex-`, which is a
	// different program, and it outlives the turn that spawned it.
	{pid: 553924, startedAt: 1785083951000, command: `C:\Users\hu\.codex\.sandbox-bin\codex-command-runner-0.145.0-alpha.27.exe --pipe-in=\\.\pipe\codex-runner-2f302fe703827877e2370f490276befb-in`},

	// The happy daemon supervising the SDK session. `claude` appears as an
	// argument, far past the two tokens that name a program.
	{pid: 643420, startedAt: 1785544384000, command: `node --no-warnings --no-deprecation C:\Users\hu\AppData\Roaming\npm\node_modules\happy\dist\index.mjs claude --happy-starting-mode remote --started-by daemon --permission-mode acceptEdits`},

	// A Claude Code tool call: bash.exe sourcing the session's shell snapshot.
	// One open session spawns these continuously, and every one of them names
	// `.claude` in a path.
	{pid: 132556, startedAt: 1784347686000, command: `"C:\Program Files\Git\bin\..\usr\bin\bash.exe" -c "source /c/Users/hu/.claude/shell-snapshots/snapshot-bash-1785551380531-g4m9he.sh 2>/dev/null || true && export TEMP='C:\Users\hu\AppData\Local\Temp'"`},

	// Our own agent, as Task Scheduler launches it.
	{pid: 636248, startedAt: 1785548421000, command: `"C:\WINDOWS\System32\wscript.exe" "C:\Users\hu\.omnitoken\omnitoken-agent-hidden.vbs"`},
	{pid: 642124, startedAt: 1785548422000, command: `C:\WINDOWS\system32\cmd.exe /c ""C:\Users\hu\.omnitoken\omnitoken-agent.bat" "`},
	{pid: 637676, startedAt: 1785548422000, command: `"C:\Users\hu\.local\bin\omnitoken.exe"  agent`},
}

// The whole point of the Windows sample: 449 processes were running on mypc
// when it was taken, two of them were sessions, and the panel was reporting
// zero.
func TestAgentSessionsOnWindowsCommandLines(t *testing.T) {
	got := agentSessions(windowsSample, 637676)
	if len(got) != 2 {
		for _, s := range got {
			t.Logf("matched pid=%d source=%s", s.PID, s.Source)
		}
		t.Fatalf("agentSessions: got %d, want 2 (both claude.exe)", len(got))
	}
	if got[0].PID != 637988 || got[0].Source != "claude-code" {
		t.Errorf("first session = %+v, want pid 637988 claude-code", got[0])
	}
	if got[1].PID != 632160 || got[1].Source != "claude-code" {
		t.Errorf("second session = %+v, want pid 632160 claude-code", got[1])
	}
}

// A backslash separates directories from the program on Windows exactly as a
// slash does on Unix, and the CLIs npm installs are nested deep enough that
// treating the whole path as one token matches nothing at all.
func TestToolForSplitsBackslashPaths(t *testing.T) {
	for cmd, want := range map[string]string{
		`C:\Users\hu\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`:                      "claude-code",
		`C:\Users\hu\.local\bin\codex.exe exec --full-auto`:                                                          "codex",
		`C:\Users\hu\AppData\Local\claude\versions\2.1.121`:                                                          "claude-code",
		`"C:\Program Files\nodejs\node.exe" C:\Users\hu\AppData\Roaming\npm\node_modules\@openai\codex\bin\codex.js`: "codex",
	} {
		if tool := toolFor(cmd); tool != want {
			t.Errorf("toolFor(%q) = %q, want %q", cmd, tool, want)
		}
	}
}

// A shim is not a session. On Windows every npm-installed CLI is started by
// one, and the binary it starts is in the same process table — so dropping the
// shim loses nothing and keeps one session from being counted twice.
//
// The Unix side has no such shim, but the rule is written for both: a shell
// invoked with a script path names the script, not a program that is running.
func TestShellShimIsNotASession(t *testing.T) {
	for _, cmd := range []string{
		`"C:\Program Files\Git\usr\bin\sh.exe" /c/Users/hu/AppData/Roaming/npm/claude`,
		`"c:\program files\git\bin\bash.exe" /c/Users/hu/AppData/Roaming/npm/codex`,
		`/bin/sh /usr/local/bin/claude`,
		`C:\WINDOWS\system32\cmd.exe /c ""C:\Users\hu\claude.bat" "`,
	} {
		if tool := toolFor(cmd); tool != "" {
			t.Errorf("toolFor(%q) = %q, want no match (shim, not session)", cmd, tool)
		}
	}
	// The binary the shim starts still counts — that is what makes dropping
	// the shim safe rather than a hole.
	if tool := toolFor(`C:\Users\hu\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\bin\claude.exe`); tool != "claude-code" {
		t.Errorf("the shimmed binary was dropped too: got %q", tool)
	}
}
