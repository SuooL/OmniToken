package collect

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Provider values produced by ProbeClaudeAuth, matching the Provider taxonomy
// in internal/model (ADR-0005 real vs equivalent cost split).
const (
	AuthAnthropicOAuth = model.ProviderAnthropicOAuth
	AuthAnthropicAPI   = model.ProviderAnthropicAPI
)

// ClaudeAuthProbe is what the machine-level probe can establish about the local
// Claude Code install. The two fields answer different questions and a caller
// needs both (ADR-0018 §3 and §4):
//
//   - Provider: how this machine pays on the first-party endpoint. Only ever
//     applied to events already known to have reached that endpoint.
//   - EndpointOverride: the endpoint is rerouted (Bedrock/Vertex/custom base
//     URL). Under an override, "no Anthropic request id" stops distinguishing a
//     relay from first-party managed hosting, so events that would otherwise be
//     called relay must fall back to unknown. Judging nothing is cheaper than
//     judging wrong.
//
// The two are mutually exclusive: an override stops the cascade before any
// credential is inspected, which is why Provider is empty whenever
// EndpointOverride is set.
type ClaudeAuthProbe struct {
	Provider         string
	EndpointOverride bool
}

// Environment variables that mean Claude Code bills through an API key.
var apiKeyVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// Environment variables that reroute Claude Code off the first-party endpoint.
// When any of these is set the traffic is Bedrock/Vertex/relay shaped and the
// model-ID fingerprint already classifies it, so probing must stay silent
// rather than mislabel it as first-party API-key usage.
var overrideVars = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_BASE_URL"}

// Injectable seams so tests never touch real processes, keychain, or /proc.
// None of these ever surfaces secret values: probing only tests key presence.
var (
	psOutput      = defaultPSOutput
	procRoot      = "/proc"
	keychainProbe = defaultKeychainProbe
	probeEnviron  = os.Environ
	probeGOOS     = runtime.GOOS
)

// ProbeClaudeAuth detects how the local Claude Code bills on the first-party
// Anthropic endpoint: AuthAnthropicOAuth (subscription), AuthAnthropicAPI
// (pay-per-token), or "" when it cannot tell. Signals are checked from highest
// confidence down; the first conclusive one wins:
//
//  1. environment of running claude processes (ps eww on darwin, /proc on linux)
//  2. the probing process's own environment
//  3. ~/.claude/settings.json env block, then OAuth credentials
//     (~/.claude/.credentials.json or the macOS keychain entry)
//
// An endpoint override (Bedrock/Vertex/custom base URL) at any level returns
// "" immediately: that traffic is already classified by model fingerprint.
// Callers should cache results via NewCachedProber; secrets are never read
// into return values, logs, or errors — only existence is tested.
func ProbeClaudeAuth() string { return ProbeClaude().Provider }

// ProbeClaude runs the cascade described on ProbeClaudeAuth and reports both
// findings: the billing channel, and whether the endpoint is rerouted at all.
// The rerouted case used to be indistinguishable from "found nothing" because
// both returned ""; ADR-0018 §4 needs them apart, since one means "stay silent"
// and the other means "actively downgrade the relay verdict to unknown".
func ProbeClaude() ClaudeAuthProbe {
	// 1. Running claude processes carry the environment that actually billed.
	// 2. The collector usually runs as the same user on the same machine.
	// 3. Persistent configuration, then stored credentials.
	for _, level := range []func() (override, apiKey bool){
		probeProcesses,
		func() (bool, bool) { return classifyEnvEntries(probeEnviron()) },
		probeSettings,
	} {
		override, apiKey := level()
		if override {
			return ClaudeAuthProbe{EndpointOverride: true}
		}
		if apiKey {
			return ClaudeAuthProbe{Provider: AuthAnthropicAPI}
		}
	}
	if probeOAuthCredentials() {
		return ClaudeAuthProbe{Provider: AuthAnthropicOAuth}
	}
	return ClaudeAuthProbe{}
}

// NewCachedProber wraps ProbeClaude with a TTL cache, since each probe may
// scan the process table. Inconclusive results are cached too.
func NewCachedProber(ttl time.Duration) func() ClaudeAuthProbe {
	var (
		mu  sync.Mutex
		val ClaudeAuthProbe
		at  time.Time
	)
	return func() ClaudeAuthProbe {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < ttl {
			return val
		}
		val = ProbeClaude()
		at = time.Now()
		return val
	}
}

// classifyEnvEntries scans KEY=value entries for billing-relevant keys.
// Values are only tested for non-emptiness and never retained.
func classifyEnvEntries(entries []string) (override, apiKey bool) {
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok || v == "" {
			continue
		}
		if contains(overrideVars, k) {
			override = true
		}
		if contains(apiKeyVars, k) {
			apiKey = true
		}
	}
	return override, apiKey
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// probeProcesses inspects the environment of running claude processes owned
// by the current user. Both results are false when no claude process is found
// or none carries a conclusive variable.
func probeProcesses() (override, apiKey bool) {
	if probeGOOS == "linux" {
		return scanLinuxProcs()
	}
	return scanPSOutput()
}

// scanPSOutput parses `ps eww -o command -u $USER` lines: the command and its
// arguments come first, then the process environment as KEY=value words.
func scanPSOutput() (override, apiKey bool) {
	out, err := psOutput()
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if !isClaudeCommand(fields) {
			continue
		}
		var envEntries []string
		for _, f := range fields {
			if strings.Contains(f, "=") {
				envEntries = append(envEntries, f)
			}
		}
		o, a := classifyEnvEntries(envEntries)
		override = override || o
		apiKey = apiKey || a
	}
	return override, apiKey
}

// isClaudeCommand reports whether the command portion of a ps line (the
// tokens before the first KEY=value word) runs claude.
func isClaudeCommand(fields []string) bool {
	for _, f := range fields {
		if strings.Contains(f, "=") {
			return false // environment section reached
		}
		base := filepath.Base(strings.Trim(f, `"'`))
		if base == "claude" || strings.HasPrefix(base, "claude") {
			return true
		}
	}
	return false
}

// scanLinuxProcs walks /proc: only the current user's processes expose a
// readable environ, which is exactly the scope we want.
func scanLinuxProcs() (override, apiKey bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false, false
	}
	for _, d := range entries {
		if !d.IsDir() || !isDigits(d.Name()) {
			continue
		}
		dir := filepath.Join(procRoot, d.Name())
		comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		if !strings.Contains(string(comm), "claude") && !strings.Contains(string(cmdline), "claude") {
			continue
		}
		environ, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			continue // other user's process; unreadable by design
		}
		o, a := classifyEnvEntries(strings.Split(string(environ), "\x00"))
		override = override || o
		apiKey = apiKey || a
	}
	return override, apiKey
}

// probeSettings checks the env block of ~/.claude/settings.json.
func probeSettings() (override, apiKey bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false, false
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return false, false
	}
	entries := make([]string, 0, len(settings.Env))
	for k, v := range settings.Env {
		entries = append(entries, k+"="+v)
	}
	return classifyEnvEntries(entries)
}

// probeOAuthCredentials tests for stored subscription (OAuth) credentials:
// the ~/.claude/.credentials.json file, or on macOS the keychain entry that
// current Claude Code versions use. Contents are only tested, never returned.
func probeOAuthCredentials() bool {
	if home, err := os.UserHomeDir(); err == nil {
		raw, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
		if err == nil && strings.Contains(string(raw), `"claudeAiOauth"`) {
			return true
		}
	}
	return probeGOOS == "darwin" && keychainProbe()
}

func defaultPSOutput() ([]byte, error) {
	args := []string{"eww", "-o", "command"}
	if u := os.Getenv("USER"); u != "" {
		args = append(args, "-u", u)
	}
	return exec.Command("ps", args...).Output()
}

// defaultKeychainProbe asks the macOS keychain whether the Claude Code
// credentials item exists. The secret itself is discarded unread.
func defaultKeychainProbe() bool {
	cmd := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
