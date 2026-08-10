package agent

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileConfig is the agent's config file (~/.omnitoken/agent.json).
// Precedence: command-line flags > environment > this file > defaults.
type FileConfig struct {
	Server             string   `json:"server"`                        // server or relay-peer base URL
	ResolveIP          string   `json:"resolve_ip,omitempty"`          // pin the server host's DNS to this IP; TLS still uses the hostname (ADR-0026 §3)
	AllowInsecureHTTP  bool     `json:"allow_insecure_http,omitempty"` // permit plaintext to a non-loopback target
	Token              string   `json:"token,omitempty"`               // ingest bearer token
	ProtocolVersion    int      `json:"protocol_version,omitempty"`    // absent means legacy v1
	DeviceID           string   `json:"device_id,omitempty"`
	DeviceToken        string   `json:"device_token,omitempty"`
	Outbox             string   `json:"outbox,omitempty"`
	OutboxMaxBytes     int64    `json:"outbox_max_bytes,omitempty"`
	Name               string   `json:"name,omitempty"`                 // device name; default hostname
	RelayListen        string   `json:"relay_listen,omitempty"`         // e.g. ":8788" to relay for peers
	RelayToken         string   `json:"relay_token,omitempty"`          // protects this relay's listener
	RelayUpstreamToken string   `json:"relay_upstream_token,omitempty"` // credential expected by the next relay hop
	IntervalSeconds    int      `json:"interval_seconds,omitempty"`     // scan interval; default 10
	ClaudeDirs         []string `json:"claude_dirs,omitempty"`          // default: auto-detect
	CodexDirs          []string `json:"codex_dirs,omitempty"`           // default: auto-detect
	State              string   `json:"state,omitempty"`                // offset state file path
	// StatuslineCachePath locates what `omnitoken statusline` leaves behind;
	// Claude's quota is read from the rate-limits file beside it (ADR-0011).
	// Default: ~/.omnitoken/statusline-cache.json, matching the status line's
	// own default and the server's.
	StatuslineCachePath string `json:"statusline_cache_path,omitempty"`
	// Since ("YYYY-MM-DD", local time) is the start of collection: events older
	// than that midnight are never reported. Empty means no window.
	//
	// The same knob SSH pull has (ADR-0015), and it is needed here for a sharper
	// reason. A push is a *self-report*, which outranks an observer's guess — so
	// pointing a fresh agent at a machine whose home directory is a synced copy
	// of another machine's lets it legitimately claim that other machine's
	// history. Measured on real hardware: 92% of a second Mac's log events
	// already existed under the first Mac's name, because 539 of 543 Codex
	// rollout files were byte-identical down to the UUID.
	//
	// So when a machine's logs are not exclusively its own, start it from the
	// day you actually want it counted from.
	Since string `json:"since,omitempty"`
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
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, err
	}
	// Fail here rather than at the first scan: a malformed window silently
	// treated as "no window" would push exactly the history the user asked to
	// leave out, and once it is attributed there is no undo.
	if _, err := fc.SinceTime(); err != nil {
		return fc, err
	}
	if version := fc.EffectiveProtocolVersion(); version != 1 && version != 2 {
		return fc, fmt.Errorf("agent config: unsupported protocol_version %d", fc.ProtocolVersion)
	}
	return fc, nil
}

// EffectiveProtocolVersion keeps every pre-v2 config explicitly compatible:
// an omitted version continues to use the legacy endpoint and token.
func (fc FileConfig) EffectiveProtocolVersion() int {
	if fc.ProtocolVersion == 0 {
		return 1
	}
	return fc.ProtocolVersion
}

// SinceTime resolves Since to the first instant to report; the zero time means
// no filtering. Same format and same reasoning as collect.SSHHost.SinceTime.
func (fc FileConfig) SinceTime() (time.Time, error) {
	if fc.Since == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", fc.Since, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("agent config: since %q is not a YYYY-MM-DD date", fc.Since)
	}
	return t, nil
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

	skeleton := FileConfig{IntervalSeconds: 10}
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

// PrepareEnrollment fills the v2 identity exactly once. Subsequent enrollment
// updates may change display metadata and server location without replacing
// the device principal or its credential.
func PrepareEnrollment(existing FileConfig, serverURL, displayName, deviceToken string) (FileConfig, error) {
	if existing.DeviceID == "" {
		deviceID, err := newUUID()
		if err != nil {
			return FileConfig{}, fmt.Errorf("generate device ID: %w", err)
		}
		existing.DeviceID = deviceID
	}
	if existing.DeviceToken == "" {
		if deviceToken == "" {
			var secret [32]byte
			if _, err := rand.Read(secret[:]); err != nil {
				return FileConfig{}, fmt.Errorf("generate device token: %w", err)
			}
			deviceToken = base64.RawURLEncoding.EncodeToString(secret[:])
		}
		existing.DeviceToken = deviceToken
	} else if deviceToken != "" && deviceToken != existing.DeviceToken {
		return FileConfig{}, fmt.Errorf("device token already exists; explicit rotation is required")
	}
	existing.ProtocolVersion = 2
	existing.Server = serverURL
	existing.Name = displayName
	return existing, nil
}

// SaveFileConfig atomically persists credentials with owner-only permissions.
func SaveFileConfig(path string, fc FileConfig) error {
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-*.json")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
