package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubProbe neutralizes every probe seam and fakes HOME so tests observe only
// what they explicitly plant. Restores everything on cleanup.
func stubProbe(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	origPS, origEnv, origKC := psOutput, probeEnviron, keychainProbe
	origGOOS, origProc := probeGOOS, procRoot
	psOutput = func() ([]byte, error) { return nil, nil }
	probeEnviron = func() []string { return nil }
	keychainProbe = func() bool { return false }
	probeGOOS = "darwin"
	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	t.Cleanup(func() {
		psOutput, probeEnviron, keychainProbe = origPS, origEnv, origKC
		probeGOOS, procRoot = origGOOS, origProc
	})
}

func writeHomeFile(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProbeProcessEnvAPIKey(t *testing.T) {
	stubProbe(t)
	psOutput = func() ([]byte, error) {
		return []byte("/bin/zsh -l\n" +
			"/usr/local/bin/claude --resume HOME=/Users/u ANTHROPIC_API_KEY=sk-test TERM=xterm\n"), nil
	}
	if got := ProbeClaudeAuth(); got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}
}

func TestProbeProcessEnvAuthToken(t *testing.T) {
	stubProbe(t)
	psOutput = func() ([]byte, error) {
		return []byte("claude HOME=/Users/u ANTHROPIC_AUTH_TOKEN=tok\n"), nil
	}
	if got := ProbeClaudeAuth(); got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}
}

func TestProbeProcessOverrideWins(t *testing.T) {
	stubProbe(t)
	for _, envs := range []string{
		"CLAUDE_CODE_USE_BEDROCK=1 ANTHROPIC_API_KEY=sk-test",
		"CLAUDE_CODE_USE_VERTEX=1",
		"ANTHROPIC_BASE_URL=https://proxy.internal ANTHROPIC_AUTH_TOKEN=tok",
	} {
		psOutput = func() ([]byte, error) {
			return []byte("claude " + envs + "\n"), nil
		}
		// Plant OAuth creds too: override must suppress every later signal.
		writeHomeFile(t, ".claude/.credentials.json", `{"claudeAiOauth":{}}`)
		if got := ProbeClaudeAuth(); got != "" {
			t.Fatalf("env %q: got %q, want \"\"", envs, got)
		}
	}
}

// "endpoint rerouted" and "found nothing" both used to come back as "", but
// ADR-0018 §4 needs them apart: one leaves the log's verdict alone, the other
// withdraws the relay verdict because a rerouted endpoint could equally be a
// real Bedrock/Vertex deployment.
func TestProbeClaudeSeparatesOverrideFromSilence(t *testing.T) {
	for _, envs := range []string{
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_VERTEX=1",
		"ANTHROPIC_BASE_URL=https://proxy.internal",
	} {
		stubProbe(t)
		psOutput = func() ([]byte, error) { return []byte("claude " + envs + "\n"), nil }
		got := ProbeClaude()
		if !got.EndpointOverride || got.Provider != "" {
			t.Errorf("env %q: got %+v, want an override with no provider", envs, got)
		}
	}

	stubProbe(t)
	if got := ProbeClaude(); got.EndpointOverride || got.Provider != "" {
		t.Errorf("nothing configured: got %+v, want a fully silent probe", got)
	}

	stubProbe(t)
	writeHomeFile(t, ".claude/.credentials.json", `{"claudeAiOauth":{}}`)
	if got := ProbeClaude(); got.EndpointOverride || got.Provider != AuthAnthropicOAuth {
		t.Errorf("OAuth credentials: got %+v, want oauth with no override", got)
	}
}

func TestProbeIgnoresNonClaudeProcesses(t *testing.T) {
	stubProbe(t)
	psOutput = func() ([]byte, error) {
		return []byte("/usr/bin/some-tool ANTHROPIC_API_KEY=sk-test\n"), nil
	}
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeShellEnv(t *testing.T) {
	stubProbe(t)
	probeEnviron = func() []string {
		return []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=sk-test"}
	}
	if got := ProbeClaudeAuth(); got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}
}

func TestProbeShellEnvBaseURLOverride(t *testing.T) {
	stubProbe(t)
	probeEnviron = func() []string {
		return []string{"ANTHROPIC_BASE_URL=https://proxy.internal", "ANTHROPIC_API_KEY=sk-test"}
	}
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeShellEnvEmptyValueIgnored(t *testing.T) {
	stubProbe(t)
	probeEnviron = func() []string { return []string{"ANTHROPIC_API_KEY="} }
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeSettingsJSON(t *testing.T) {
	stubProbe(t)
	writeHomeFile(t, ".claude/settings.json",
		`{"env":{"ANTHROPIC_API_KEY":"sk-test"},"model":"opus"}`)
	if got := ProbeClaudeAuth(); got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}
}

func TestProbeSettingsJSONOverride(t *testing.T) {
	stubProbe(t)
	writeHomeFile(t, ".claude/settings.json",
		`{"env":{"ANTHROPIC_BASE_URL":"https://proxy.internal","ANTHROPIC_API_KEY":"sk-test"}}`)
	writeHomeFile(t, ".claude/.credentials.json", `{"claudeAiOauth":{}}`)
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeOAuthCredentialsFile(t *testing.T) {
	stubProbe(t)
	writeHomeFile(t, ".claude/.credentials.json",
		`{"claudeAiOauth":{"accessToken":"redacted"}}`)
	if got := ProbeClaudeAuth(); got != AuthAnthropicOAuth {
		t.Fatalf("got %q, want %q", got, AuthAnthropicOAuth)
	}
}

func TestProbeOAuthKeychain(t *testing.T) {
	stubProbe(t)
	keychainProbe = func() bool { return true }
	if got := ProbeClaudeAuth(); got != AuthAnthropicOAuth {
		t.Fatalf("got %q, want %q", got, AuthAnthropicOAuth)
	}
}

func TestProbeKeychainSkippedOffDarwin(t *testing.T) {
	stubProbe(t)
	probeGOOS = "linux"
	keychainProbe = func() bool {
		t.Fatal("keychain must not be probed on linux")
		return true
	}
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeNothingFound(t *testing.T) {
	stubProbe(t)
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("got %q, want \"\"", got)
	}
}

func TestProbeLinuxProcScan(t *testing.T) {
	stubProbe(t)
	probeGOOS = "linux"
	proc := t.TempDir()
	procRoot = proc

	writeProc := func(pid, comm, environ string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"comm": comm + "\n", "cmdline": comm + "\x00", "environ": environ,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeProc("100", "bash", "ANTHROPIC_API_KEY=sk-test\x00PATH=/bin")      // not claude: ignored
	writeProc("200", "claude", "HOME=/home/u\x00ANTHROPIC_API_KEY=sk-test") // hit

	if got := ProbeClaudeAuth(); got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}

	// Same tree plus an overriding claude process: probe must back off.
	writeProc("300", "claude", "CLAUDE_CODE_USE_BEDROCK=1\x00PATH=/bin")
	if got := ProbeClaudeAuth(); got != "" {
		t.Fatalf("with override: got %q, want \"\"", got)
	}
}

func TestNewCachedProber(t *testing.T) {
	stubProbe(t)
	calls := 0
	psOutput = func() ([]byte, error) {
		calls++
		return []byte("claude ANTHROPIC_API_KEY=sk-test\n"), nil
	}
	probe := NewCachedProber(time.Hour)
	if got := probe().Provider; got != AuthAnthropicAPI {
		t.Fatalf("got %q, want %q", got, AuthAnthropicAPI)
	}
	if got := probe().Provider; got != AuthAnthropicAPI {
		t.Fatalf("cached: got %q, want %q", got, AuthAnthropicAPI)
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times, want 1 (cached)", calls)
	}
}
