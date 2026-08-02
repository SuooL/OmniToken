package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/pricing"
)

type Config struct {
	Listen     string `json:"listen"`
	DBPath     string `json:"db"`
	StatePath  string `json:"state"`
	MirrorRoot string `json:"mirror"`
	Token      string `json:"token"` // shared bearer token for push-mode agents; empty = no auth
	ReadToken  string `json:"read_token,omitempty"`
	AdminToken string `json:"admin_token,omitempty"`
	DeviceName string `json:"device_name"`
	// Timezone is the IANA name whose midnight defines a day for every
	// daily/weekly/monthly aggregation (ADR-0021). Empty follows the host.
	Timezone string `json:"timezone,omitempty"`

	// location caches the parsed Timezone. Resolved during validate() so a
	// typo fails the load rather than surfacing as a quietly shifted day.
	location *time.Location `json:"-"`

	readTokenConfigured  bool `json:"-"`
	adminTokenConfigured bool `json:"-"`

	// deviceNameFile keeps what the config file actually said, so re-resolving
	// with a flag cannot mistake an earlier resolution's output for
	// configuration. deviceIdentity records where the effective name came from.
	deviceNameFile string         `json:"-"`
	deviceIdentity DeviceIdentity `json:"-"`

	// PricingOverrides: per-1M-token USD prices keyed by model id (ADR-0005).
	PricingOverrides map[string]pricing.Override `json:"pricing_overrides,omitempty"`
	// WorktimeIdleMinutes: idle gap that stops the work clock (F8); default 5.
	WorktimeIdleMinutes int `json:"worktime_idle_minutes,omitempty"`
	// ProxyListen runs the local API proxy (F14) inside the server, e.g.
	// "127.0.0.1:8899"; empty = off. The agent has the same knob. Hosting it
	// here is for the common single-machine setup: the server already collects
	// this machine's logs, and running an agent purely for the proxy would
	// scan them a second time.
	ProxyListen string `json:"proxy_listen,omitempty"`
	// ProxyUpstreams maps a path prefix to an upstream base URL, merged over
	// the built-in anthropic/openai entries.
	ProxyUpstreams map[string]string `json:"proxy_upstreams,omitempty"`

	// StatuslineCachePath locates what `omnitoken statusline` leaves behind.
	// Quota is read from a file beside it (ADR-0011) rather than polled from
	// Anthropic, so this is the only knob the quota channel needs.
	StatuslineCachePath string `json:"statusline_cache_path,omitempty"`

	Collect struct {
		IntervalSeconds int               `json:"interval_seconds"`
		Local           *bool             `json:"local"`                // scan this machine's own logs; default true
		LocalDirs       []string          `json:"local_dirs,omitempty"` // Claude Code log dirs
		CodexDirs       []string          `json:"codex_dirs,omitempty"`
		SSHHosts        []collect.SSHHost `json:"ssh_hosts,omitempty"`
	} `json:"collect"`
}

func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omnitoken")
}

func DefaultLocalClaudeDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, ".claude", "projects")}
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "projects"))
	}
	dirs = append(dirs, filepath.Join(home, ".config", "claude", "projects"))
	return dirs
}

func DefaultLocalCodexDirs() []string {
	home, _ := os.UserHomeDir()
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		root = filepath.Join(home, ".codex")
	}
	return []string{filepath.Join(root, "sessions"), filepath.Join(root, "archived_sessions")}
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		_, cfg.readTokenConfigured = fields["read_token"]
		_, cfg.adminTokenConfigured = fields["admin_token"]
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate rejects configuration that would silently do the wrong thing. A
// mistyped ssh_hosts[].since is the case that matters: ignoring it would
// back-import the whole history the user asked to leave out (ADR-0015), and
// there is no way to take that back once it is in the database.
func (c *Config) validate() error {
	for _, h := range c.Collect.SSHHosts {
		if _, err := h.SinceTime(); err != nil {
			return err
		}
	}
	return c.resolveTimezone()
}

// resolveTimezone parses Timezone once, at load. A typo must not survive to
// runtime: an unknown zone that quietly became UTC would move every daily,
// weekly and monthly bucket by hours, and nothing on screen would say so.
func (c *Config) resolveTimezone() error {
	if strings.TrimSpace(c.Timezone) == "" {
		c.location = time.Local
		return nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf(`timezone %q: %w (需 IANA 名,如 "America/New_York")`, c.Timezone, err)
	}
	c.location = loc
	return nil
}

// Location is the zone whose midnight starts a day for every aggregation.
func (c *Config) Location() *time.Location {
	if c.location == nil {
		return time.Local
	}
	return c.location
}

// ApplyTimezone pins the aggregation day boundary for this process and returns
// the zone it settled on, for the caller to log (ADR-0021).
//
// It assigns the package-level `time.Local` on purpose. That is the only switch
// reaching both halves of the split: the nine Go sites that call
// `now.Location()`, and the eight SQL sites using `date(..., 'localtime')`,
// which the modernc driver resolves through `time.Local`. Passing a Location
// down to the Go callers would leave the SQL half on the host zone, and the two
// would disagree about which day a row belongs to.
//
// Call once, during startup, before the store opens or any query runs; nothing
// writes it afterwards.
func ApplyTimezone(c *Config) string {
	time.Local = c.Location()
	return time.Local.String()
}

func (c *Config) applyDefaults() {
	// A legacy config had one shared token. Preserve its effective behavior
	// only when the new scoped fields are absent; an explicitly empty scoped
	// field remains empty and is caught by the non-loopback safety check.
	if !c.readTokenConfigured {
		c.ReadToken = c.Token
	}
	if !c.adminTokenConfigured {
		c.AdminToken = c.Token
	}

	dd := DataDir()
	if c.Listen == "" {
		// Loopback, not `:8787`. The old default listened on every interface
		// while the read endpoints were unauthenticated (ADR-0008), so a fresh
		// install published its owner's entire usage history to the network —
		// the insecure setup was the one you got by doing nothing.
		// Reaching this server from another machine is now a deliberate act:
		// either set a token (ADR-0016) or tunnel/relay to it (ADR-0003).
		c.Listen = "127.0.0.1:8787"
	}
	if c.DBPath == "" {
		c.DBPath = filepath.Join(dd, "omnitoken.db")
	}
	if c.StatePath == "" {
		c.StatePath = filepath.Join(dd, "server-state.json")
	}
	if c.MirrorRoot == "" {
		c.MirrorRoot = filepath.Join(dd, "mirror")
	}
	// One chain for both roles (ADR-0019 §7): serve used to fall back to the
	// hostname here while the agent fell back to it somewhere else, and the two
	// paths disagreed about whether config.json's device_name counted.
	c.deviceNameFile = c.DeviceName
	c.ResolveDeviceName("")
	if c.Collect.IntervalSeconds <= 0 {
		c.Collect.IntervalSeconds = 15
	}
	if c.WorktimeIdleMinutes <= 0 {
		c.WorktimeIdleMinutes = 5
	}
	if c.StatuslineCachePath == "" {
		home, _ := os.UserHomeDir()
		c.StatuslineCachePath = filepath.Join(home, ".omnitoken", "statusline-cache.json")
	}
	if len(c.Collect.LocalDirs) == 0 {
		c.Collect.LocalDirs = DefaultLocalClaudeDirs()
	}
	if len(c.Collect.CodexDirs) == 0 {
		c.Collect.CodexDirs = DefaultLocalCodexDirs()
	}
}

// ResolveDeviceName sets this server's device name from the shared chain and
// returns where it came from. The config file's own `device_name` occupies the
// config slot, so there is nothing left for the shared-config slot to add.
func (c *Config) ResolveDeviceName(flagValue string) DeviceIdentity {
	c.deviceIdentity = ResolveDeviceName(DeviceNameInputs{
		Flag:     flagValue,
		Env:      os.Getenv(DeviceNameEnv),
		Config:   c.deviceNameFile,
		Hostname: os.Hostname,
	})
	c.DeviceName = c.deviceIdentity.Name
	return c.deviceIdentity
}

// DeviceIdentity reports how the effective device name was arrived at.
func (c *Config) DeviceIdentity() DeviceIdentity { return c.deviceIdentity }

func (c *Config) LocalEnabled() bool {
	return c.Collect.Local == nil || *c.Collect.Local
}

// WriteDefaultConfig writes a config file filled with the effective defaults,
// so a first-time user has something to read and edit instead of guessing
// which fields exist. Values are the resolved ones (absolute paths, this
// host's name) — the file is per-machine anyway, and showing where data
// actually lands is more useful than leaving blanks.
//
// It refuses to touch an existing file: config may hold a token the user
// typed, and silently rewriting it would be destructive.
func WriteDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}

	cfg := &Config{}
	cfg.applyDefaults()
	// applyDefaults leaves Local nil (nil means enabled). Spell it out, or the
	// generated file would show `"local": null` and read like a bug.
	enabled := true
	cfg.Collect.Local = &enabled

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: this file carries the ingest token.
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
