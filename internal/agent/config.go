package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileConfig is the agent's config file (~/.omnitoken/agent.json).
// Precedence: command-line flags > environment > this file > defaults.
type FileConfig struct {
	Server          string   `json:"server"`                     // server or relay-peer base URL
	Token           string   `json:"token,omitempty"`            // ingest bearer token
	Name            string   `json:"name,omitempty"`             // device name; default hostname
	RelayListen     string   `json:"relay_listen,omitempty"`     // e.g. ":8788" to relay for peers
	IntervalSeconds int      `json:"interval_seconds,omitempty"` // scan interval; default 15
	ClaudeDirs      []string `json:"claude_dirs,omitempty"`      // default: auto-detect
	CodexDirs       []string `json:"codex_dirs,omitempty"`       // default: auto-detect
	State           string   `json:"state,omitempty"`            // offset state file path
	// Local API proxy (F14): point your scripts' base_url at
	// http://<proxy_listen>/anthropic (or /openai, or a custom prefix) to
	// capture direct API usage with exact TTFT/duration.
	ProxyListen    string            `json:"proxy_listen,omitempty"`    // e.g. "127.0.0.1:8899"; empty = off
	ProxyUpstreams map[string]string `json:"proxy_upstreams,omitempty"` // prefix -> upstream base, merged over builtins
}

// LoadFileConfig reads path if it exists; a missing file is not an error.
func LoadFileConfig(path string) (FileConfig, error) {
	var fc FileConfig
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fc, nil
	}
	if err != nil {
		return fc, err
	}
	err = json.Unmarshal(data, &fc)
	return fc, err
}

// WriteSkeletonConfig writes a starter agent.json so the user has a file to
// edit rather than a path they have to create by hand.
//
// Server is left empty on purpose: filling in a placeholder host would make a
// re-run fail while trying to reach something that does not exist, instead of
// repeating the clear "server URL is required" message.
//
// It refuses to touch an existing file — that file may already hold a token.
func WriteSkeletonConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}

	skeleton := FileConfig{IntervalSeconds: 15}
	data, err := json.MarshalIndent(skeleton, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: this file carries the ingest token.
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
