package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config this fleet actually runs: cc-switch renamed the provider block and
// pointed base_url at its own local proxy, while keeping the ChatGPT
// credentials. The name says nothing; requires_openai_auth says everything.
const realWorldConfig = `
model = "gpt-5.6-sol"
model_provider = "cc-switch-official"

[marketplaces.openai-bundled]
enabled = true

[model_providers.cc-switch-official]
name = "OpenAI"
requires_openai_auth = true
supports_websockets = false
base_url = "http://127.0.0.1:15721/v1"

[model_providers.sub2api]
name = "sub2api"
base_url = "https://example.invalid/v1"
env_key = "SUB2API_KEY"
`

func TestSubscriptionProviderIDsReadsRequiresOpenAIAuth(t *testing.T) {
	got := subscriptionProviderIDs(strings.NewReader(realWorldConfig))
	if !got["cc-switch-official"] {
		t.Errorf("cc-switch-official missing: %v", got)
	}
	if got["sub2api"] {
		t.Errorf("a relay with its own env_key must not be trusted: %v", got)
	}
	if len(got) != 1 {
		t.Errorf("got = %v, want exactly the one authenticated block", got)
	}
}

// A key belonging to a later table must not be attributed to the provider above
// it — that would trust a block on the strength of someone else's setting.
func TestSubscriptionProviderIDsStopsAtTheNextTable(t *testing.T) {
	cfg := `
[model_providers.relay]
base_url = "https://example.invalid/v1"

[tools]
requires_openai_auth = true
`
	if got := subscriptionProviderIDs(strings.NewReader(cfg)); len(got) != 0 {
		t.Errorf("got = %v, want nothing trusted", got)
	}
}

func TestSubscriptionProviderIDsHandlesQuotingAndComments(t *testing.T) {
	cfg := `
["model_providers"."my-provider"]           # not the form Codex writes
[model_providers."quoted-name"]
requires_openai_auth = true                 # keeps the ChatGPT session
base_url = "https://example.invalid/v1#frag"

[model_providers.commented-out]
# requires_openai_auth = true
`
	got := subscriptionProviderIDs(strings.NewReader(cfg))
	if !got["quoted-name"] {
		t.Errorf("quoted table name not recognised: %v", got)
	}
	if got["commented-out"] {
		t.Errorf("a commented-out key must not count: %v", got)
	}
}

// false, or the key being absent entirely, both mean "this block does not spend
// the subscription". Neither may be read as consent.
func TestSubscriptionProviderIDsRequiresLiteralTrue(t *testing.T) {
	for _, value := range []string{"false", `"true"`, "1", ""} {
		cfg := "[model_providers.p]\nrequires_openai_auth = " + value + "\n"
		if got := subscriptionProviderIDs(strings.NewReader(cfg)); len(got) != 0 {
			t.Errorf("value %q: got = %v, want nothing trusted", value, got)
		}
	}
}

func TestProbeCodexAuthReadsConfigBesideSessions(t *testing.T) {
	// Point CODEX_HOME at an empty directory so the test reads the fixture below
	// and never the developer's own ~/.codex/config.toml.
	t.Setenv("CODEX_HOME", t.TempDir())
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(realWorldConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	probe := ProbeCodexAuth([]string{filepath.Join(home, "sessions")})
	if !probe.Trusts("cc-switch-official") {
		t.Errorf("probe = %+v, want the renamed block trusted", probe)
	}
	if probe.Trusts("sub2api") || probe.Trusts("") {
		t.Errorf("probe = %+v, trusted something it should not", probe)
	}
}

// A machine with no Codex install is the ordinary case, not an error, and it
// must leave the parser exactly where it was: built-in id only.
func TestProbeCodexAuthOnMissingConfigTrustsNothing(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	probe := ProbeCodexAuth([]string{filepath.Join(t.TempDir(), "sessions")})
	if probe.SubscriptionProviders != nil {
		t.Errorf("probe = %+v, want an empty set", probe)
	}
	if probe.Trusts("openai") {
		t.Error("an empty probe must trust nothing at all — the parser applies the built-in rule itself")
	}
}
