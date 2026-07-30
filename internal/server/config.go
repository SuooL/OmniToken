package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suool/omnitoken/internal/collect"
	"github.com/suool/omnitoken/internal/pricing"
)

type Config struct {
	Listen     string `json:"listen"`
	DBPath     string `json:"db"`
	StatePath  string `json:"state"`
	MirrorRoot string `json:"mirror"`
	Token      string `json:"token"` // shared bearer token for push-mode agents; empty = no auth
	DeviceName string `json:"device_name"`

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
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	dd := DataDir()
	if c.Listen == "" {
		c.Listen = ":8787"
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
	if c.DeviceName == "" {
		if h, err := os.Hostname(); err == nil {
			c.DeviceName = h
		} else {
			c.DeviceName = "server"
		}
	}
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
