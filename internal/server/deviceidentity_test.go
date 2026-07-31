package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixedHostname(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func TestResolveDeviceNameFollowsOneChain(t *testing.T) {
	full := DeviceNameInputs{
		Flag: "from-flag", Env: "from-env", Config: "from-config",
		SharedConfig: "from-shared", Hostname: fixedHostname("from-hostname"),
	}
	cases := []struct {
		name   string
		mutate func(*DeviceNameInputs)
		want   string
		source DeviceNameSource
	}{
		{"an explicit flag wins", func(*DeviceNameInputs) {}, "from-flag", DeviceNameFromFlag},
		{"then the environment", func(in *DeviceNameInputs) { in.Flag = "" }, "from-env", DeviceNameFromEnv},
		{"then this role's own config", func(in *DeviceNameInputs) { in.Flag, in.Env = "", "" },
			"from-config", DeviceNameFromConfig},
		{"then the shared server config", func(in *DeviceNameInputs) { in.Flag, in.Env, in.Config = "", "", "" },
			"from-shared", DeviceNameFromSharedConfig},
		{"and only then the hostname", func(in *DeviceNameInputs) {
			in.Flag, in.Env, in.Config, in.SharedConfig = "", "", "", ""
		}, "from-hostname", DeviceNameFromHostname},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := full
			tc.mutate(&in)
			got := ResolveDeviceName(in)
			if got.Name != tc.want || got.Source != tc.source {
				t.Fatalf("resolved %+v, want %q from %q", got, tc.want, tc.source)
			}
			if got.FellBackToHostname() != (tc.source == DeviceNameFromHostname) {
				t.Fatalf("FellBackToHostname = %v for source %q", got.FellBackToHostname(), got.Source)
			}
		})
	}
}

// Whitespace around a configured name would otherwise create a third identity
// that looks identical on screen.
func TestResolveDeviceNameTrimsAndSkipsBlankCandidates(t *testing.T) {
	got := ResolveDeviceName(DeviceNameInputs{
		Config: "   ", SharedConfig: "  suool-mac  ", Hostname: fixedHostname("host.local"),
	})
	if got.Name != "suool-mac" || got.Source != DeviceNameFromSharedConfig {
		t.Fatalf("resolved %+v, want the trimmed shared-config value", got)
	}
}

// A machine with no usable hostname must still get a name rather than an empty
// device column; naming it in one place keeps that fallback from being invented
// separately by each role.
func TestResolveDeviceNameSurvivesAnUnusableHostname(t *testing.T) {
	got := ResolveDeviceName(DeviceNameInputs{
		Hostname: func() (string, error) { return "", os.ErrNotExist },
	})
	if got.Name == "" {
		t.Fatal("resolution must always yield a name")
	}
	if got.Source != DeviceNameLastResort {
		t.Fatalf("source = %q, want the last-resort marker so the log can say so", got.Source)
	}
	if got.FellBackToHostname() {
		t.Fatal("no hostname was available; the log line must not claim one was used")
	}
}

// The bug ADR-0019 §7 is about: one machine, two identities, because `serve`
// read config.json's device_name and `agent` did not. Same machine state must
// now give both roles the same answer.
func TestServeAndAgentAgreeOnThisMachinesIdentity(t *testing.T) {
	t.Setenv(DeviceNameEnv, "")
	dir := t.TempDir()
	sharedPath := filepath.Join(dir, "config.json")
	writeConfigFile(t, sharedPath, map[string]any{"device_name": "suool-mac"})

	cfg, err := LoadConfig(sharedPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// The agent's own config file carries no name — exactly the state that
	// produced the second identity.
	agent := LocalDeviceName("", "", sharedPath)

	if cfg.DeviceName != "suool-mac" {
		t.Fatalf("serve resolved %q, want the configured device_name", cfg.DeviceName)
	}
	if agent.Name != cfg.DeviceName {
		t.Fatalf("agent resolved %q but serve resolved %q — that split is the whole bug",
			agent.Name, cfg.DeviceName)
	}
	if agent.FellBackToHostname() {
		t.Fatal("the agent fell back to the hostname while a device_name was configured")
	}
}

func TestLocalDeviceNamePrefersItsOwnConfigOverTheSharedOne(t *testing.T) {
	t.Setenv(DeviceNameEnv, "")
	sharedPath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, sharedPath, map[string]any{"device_name": "suool-mac"})
	got := LocalDeviceName("", "agent-json-name", sharedPath)
	if got.Name != "agent-json-name" || got.Source != DeviceNameFromConfig {
		t.Fatalf("resolved %+v, want the role's own config to win", got)
	}
}

func TestDeviceNameFromConfigFileIsQuietAboutMissingOrBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	if got := DeviceNameFromConfigFile(filepath.Join(dir, "absent.json")); got != "" {
		t.Fatalf("missing config yielded %q", got)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unreadable shared config must not stop an agent from starting: the
	// chain simply carries on to the hostname.
	if got := DeviceNameFromConfigFile(broken); got != "" {
		t.Fatalf("unparseable config yielded %q", got)
	}
}

// The environment variable used to reach only the agent, which is another way
// for the same machine to end up with two names.
func TestServeHonoursTheDeviceNameEnvironmentVariable(t *testing.T) {
	t.Setenv(DeviceNameEnv, "from-env")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DeviceName != "from-env" {
		t.Fatalf("device name = %q, want the environment override", cfg.DeviceName)
	}
	if cfg.DeviceIdentity().Source != DeviceNameFromEnv {
		t.Fatalf("provenance = %q, want %q", cfg.DeviceIdentity().Source, DeviceNameFromEnv)
	}
}

// A flag beats everything, and re-resolving with one does not let an earlier
// resolution masquerade as configuration.
func TestConfigDeviceNameFlagOverridesAndKeepsProvenanceHonest(t *testing.T) {
	t.Setenv(DeviceNameEnv, "")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.DeviceIdentity().FellBackToHostname() {
		t.Fatalf("with nothing configured the name must come from the hostname, got %+v", cfg.DeviceIdentity())
	}
	identity := cfg.ResolveDeviceName("mac-mini")
	if cfg.DeviceName != "mac-mini" || identity.Source != DeviceNameFromFlag {
		t.Fatalf("flag override gave %+v", identity)
	}
	// Resolving again with no flag must fall back to the hostname, not to the
	// value the previous resolution happened to leave in the field.
	if again := cfg.ResolveDeviceName(""); !again.FellBackToHostname() {
		t.Fatalf("re-resolution reported %+v; a resolved value is not configuration", again)
	}
}

func writeConfigFile(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
