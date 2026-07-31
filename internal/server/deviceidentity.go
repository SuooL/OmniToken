package server

import (
	"encoding/json"
	"os"
	"strings"
)

// This machine's identity, resolved in exactly one place (ADR-0019 §7).
//
// The two-identity bug this fixes was not caused by the hostname fallback. It
// was caused by there being two independent resolutions of the same question:
// `serve` read `device_name` out of ~/.omnitoken/config.json and `agent` did
// not, so the same Mac reported as `suool-mac` through one role and
// `JasonHudeMacBook-Pro.local` through the other, and the database honestly
// recorded two machines.
//
// Keeping the fallback is deliberate: a headless server should not refuse to
// start over a display string. What cannot stay is the second answer — so both
// roles now call ResolveDeviceName with the same chain, and a fallback says so
// in the startup log instead of quietly inventing a name.

// DeviceNameEnv overrides the configured name for both roles. It used to reach
// the agent only, which is another way for one machine to acquire two names.
const DeviceNameEnv = "OMNITOKEN_NAME"

// DeviceNameSource records which link of the chain supplied the name. It exists
// for the log line: "where did this name come from" is the first question when
// a machine turns up twice.
type DeviceNameSource string

const (
	DeviceNameFromFlag         DeviceNameSource = "flag"
	DeviceNameFromEnv          DeviceNameSource = "env"
	DeviceNameFromConfig       DeviceNameSource = "config"
	DeviceNameFromSharedConfig DeviceNameSource = "shared-config"
	DeviceNameFromHostname     DeviceNameSource = "hostname"
	// DeviceNameLastResort is the name used when even the hostname is
	// unavailable. Returning an empty device would put rows under "" and make
	// them invisible on every per-device view.
	DeviceNameLastResort DeviceNameSource = "last-resort"
)

const lastResortDeviceName = "server"

// DeviceIdentity is a resolved name plus where it came from.
type DeviceIdentity struct {
	Name   string
	Source DeviceNameSource
}

// FellBackToHostname reports whether nothing was configured. Callers log this
// at startup so the user sees the hostname being adopted *before* they see two
// machines in the panel.
func (d DeviceIdentity) FellBackToHostname() bool {
	return d.Source == DeviceNameFromHostname
}

// DeviceNameInputs are the candidates, highest precedence first. Config is the
// value from the role's own config file (`device_name` for serve, `name` for
// the agent); SharedConfig is `device_name` from ~/.omnitoken/config.json,
// which is what lets an agent with no name of its own agree with the server
// running beside it.
type DeviceNameInputs struct {
	Flag         string
	Env          string
	Config       string
	SharedConfig string
	Hostname     func() (string, error)
}

// ResolveDeviceName applies the single precedence chain:
// flag > environment > this role's config > the shared server config > hostname.
func ResolveDeviceName(in DeviceNameInputs) DeviceIdentity {
	for _, candidate := range []struct {
		value  string
		source DeviceNameSource
	}{
		{in.Flag, DeviceNameFromFlag},
		{in.Env, DeviceNameFromEnv},
		{in.Config, DeviceNameFromConfig},
		{in.SharedConfig, DeviceNameFromSharedConfig},
	} {
		// Trimmed, because " suool-mac" and "suool-mac" would be two identities
		// that render identically — the exact failure this file exists to stop.
		if name := strings.TrimSpace(candidate.value); name != "" {
			return DeviceIdentity{Name: name, Source: candidate.source}
		}
	}
	lookup := in.Hostname
	if lookup == nil {
		lookup = os.Hostname
	}
	if host, err := lookup(); err == nil {
		if name := strings.TrimSpace(host); name != "" {
			return DeviceIdentity{Name: name, Source: DeviceNameFromHostname}
		}
	}
	return DeviceIdentity{Name: lastResortDeviceName, Source: DeviceNameLastResort}
}

// LocalDeviceName is the entry point for roles whose own config file is not the
// shared one — today that is `omnitoken agent`. It fills in the environment and
// the shared config so the caller cannot forget a link of the chain.
func LocalDeviceName(flagValue, roleConfigValue, sharedConfigPath string) DeviceIdentity {
	return ResolveDeviceName(DeviceNameInputs{
		Flag:         flagValue,
		Env:          os.Getenv(DeviceNameEnv),
		Config:       roleConfigValue,
		SharedConfig: DeviceNameFromConfigFile(sharedConfigPath),
		Hostname:     os.Hostname,
	})
}

// DeviceNameFromConfigFile reads just `device_name` out of a server config.
// Every failure is silent on purpose: an absent or hand-broken config.json is
// not a reason for an agent to refuse to run, and the chain simply continues.
// LoadConfig, not this, is what validates a config the user asked to use.
func DeviceNameFromConfigFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		DeviceName string `json:"device_name"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.DeviceName)
}
