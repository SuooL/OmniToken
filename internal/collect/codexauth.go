package collect

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Codex billing evidence read from this machine's config.toml (ADR-0033).
//
// The problem it solves: ADR-0018 decided a Codex session is subscription
// traffic only when its `model_provider` is Codex's own built-in id, spelled
// exactly `openai`. That was right for the evidence available then — the id is
// the only thing the rollout log says about where the request went — but it
// breaks the moment the id is renamed, and config switchers rename it as a
// matter of course. On this fleet `cc-switch-official` is the official ChatGPT
// endpoint under a new name, and the old rule filed a month of subscription
// traffic under "third-party relay": the 5-hour card counted 0 tokens while the
// provider itself reported the window 73% full.
//
// The name was never the evidence. What the machine can actually show is the
// provider block the name resolves to:
//
//	[model_providers.cc-switch-official]
//	requires_openai_auth = true
//	base_url = "http://127.0.0.1:15721/v1"
//
// `requires_openai_auth = true` means Codex attaches the ChatGPT account's
// credentials to the request. That is the definition of subscription traffic,
// and it stays true of the local proxy above: whoever finally answers, the
// window being spent is the account's. A relay that does NOT get those
// credentials cannot set this and is paid for some other way, which is exactly
// the distinction ADR-0018 wanted.
//
// Machine-level evidence, so — like the Claude probe — it may only be applied
// to logs read from this machine (RefineProvider's rule 1).

// CodexAuthProbe is the set of `model_provider` ids on this machine that spend
// the ChatGPT subscription. A nil or empty set means "nothing established",
// which leaves the parser on its built-in-id-only rule.
type CodexAuthProbe struct {
	SubscriptionProviders map[string]bool
}

// Trusts reports whether a rollout's declared model_provider is one of the
// blocks this machine authenticates with the ChatGPT account.
func (p CodexAuthProbe) Trusts(provider string) bool {
	return p.SubscriptionProviders[strings.TrimSpace(provider)]
}

// codexHomes lists the directories that may hold config.toml, most specific
// first: $CODEX_HOME, then ~/.codex, then the parent of each configured session
// directory — a non-default `codex_dirs` points at `<home>/sessions`, and its
// config sits beside it.
func codexHomes(sessionDirs []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	// CODEX_HOME replaces the default location rather than adding to it — that
	// is what it means to Codex, and reading the default too would let a stale
	// ~/.codex/config.toml speak for an installation that has moved.
	if explicit := os.Getenv("CODEX_HOME"); explicit != "" {
		add(explicit)
	} else if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".codex"))
	}
	for _, d := range sessionDirs {
		if base := filepath.Base(d); base == "sessions" {
			add(filepath.Dir(d))
		} else {
			add(d)
		}
	}
	return out
}

// ProbeCodexAuth reads every config.toml reachable from sessionDirs and
// collects the provider ids whose block requires the ChatGPT credentials.
//
// Unreadable or absent files are not errors: a machine with no Codex install is
// the normal case, and the result is simply an empty set.
func ProbeCodexAuth(sessionDirs []string) CodexAuthProbe {
	providers := map[string]bool{}
	for _, home := range codexHomes(sessionDirs) {
		f, err := os.Open(filepath.Join(home, "config.toml"))
		if err != nil {
			continue
		}
		for id := range subscriptionProviderIDs(f) {
			providers[id] = true
		}
		f.Close()
	}
	if len(providers) == 0 {
		return CodexAuthProbe{}
	}
	return CodexAuthProbe{SubscriptionProviders: providers}
}

// subscriptionProviderIDs scans a config.toml for `[model_providers.X]` blocks
// carrying `requires_openai_auth = true`.
//
// A hand-rolled scan rather than a TOML dependency: the shape needed here is
// one table header and one boolean key, and the file is read for exactly this
// one fact. Nothing else in it is interpreted, so a construct this scanner does
// not understand costs at most one unrecognised provider — which falls back to
// the pre-existing built-in-id rule rather than to a wrong answer.
func subscriptionProviderIDs(r io.Reader) map[string]bool {
	const prefix = "model_providers."
	out := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	current := ""
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Any new table header ends the previous provider's block, including
			// `[[x]]` and unrelated tables — otherwise a key from a later section
			// would be attributed to the provider above it.
			current = ""
			if id, ok := strings.CutPrefix(strings.Trim(line, "[]"), prefix); ok {
				current = strings.Trim(strings.TrimSpace(id), `"'`)
			}
			continue
		}
		if current == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "requires_openai_auth" {
			continue
		}
		if strings.TrimSpace(value) == "true" {
			out[current] = true
		}
	}
	return out
}

// stripTOMLComment drops a trailing `#` comment, leaving `#` inside a quoted
// value alone — base_url values carry fragments and colours often enough that
// cutting on the first `#` would truncate one.
func stripTOMLComment(line string) string {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// NewCachedCodexProber wraps ProbeCodexAuth with a TTL cache. The file is small
// but the probe runs on every scan cycle, and a config switcher rewriting it
// mid-session should be picked up within a cycle or two rather than never.
func NewCachedCodexProber(sessionDirs []string, ttl time.Duration) func() CodexAuthProbe {
	var (
		mu  sync.Mutex
		val CodexAuthProbe
		at  time.Time
	)
	return func() CodexAuthProbe {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < ttl {
			return val
		}
		val = ProbeCodexAuth(sessionDirs)
		at = time.Now()
		return val
	}
}
