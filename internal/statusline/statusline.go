// Package statusline renders a one-line Claude Code status line that mixes
// this session's numbers (from Claude Code's own stdin payload) with
// cross-device totals and authoritative quota from an OmniToken server —
// the part single-machine status lines cannot know.
//
// Claude Code invokes this on every render, so the hard rule is: never make
// the user wait. Budget ~300ms total, 200ms on the network, and fall back to
// the last cached response (marked stale) whenever anything is slow or down.
package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	fetchTimeout = 200 * time.Millisecond
	cacheFresh   = 10 * time.Second
	staleMark    = "⟳"
)

type Config struct {
	Server    string   `json:"server"`
	Token     string   `json:"token,omitempty"`
	Segments  []string `json:"segments,omitempty"`  // order; default below
	Separator string   `json:"separator,omitempty"` // default " · "
	CachePath string   `json:"cache_path,omitempty"`
	NoColor   bool     `json:"no_color,omitempty"`
}

var defaultSegments = []string{"session", "today", "quota"}

func (c *Config) applyDefaults() {
	if c.Server == "" {
		c.Server = "http://127.0.0.1:8787"
	}
	c.Server = strings.TrimSuffix(c.Server, "/")
	if len(c.Segments) == 0 {
		c.Segments = defaultSegments
	}
	if c.Separator == "" {
		c.Separator = " · "
	}
	if c.CachePath == "" {
		home, _ := os.UserHomeDir()
		c.CachePath = filepath.Join(home, ".omnitoken", "statusline-cache.json")
	}
	if os.Getenv("NO_COLOR") != "" {
		c.NoColor = true
	}
}

// LoadConfig reads path if present; a missing file is not an error.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &c); err != nil {
			return c, err
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	return c, nil
}

// sessionInput is the subset of Claude Code's status-line payload we use.
type sessionInput struct {
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost struct {
		TotalCostUSD    float64 `json:"total_cost_usd"`
		TotalDurationMS int64   `json:"total_duration_ms"`
	} `json:"cost"`
	ContextWindow struct {
		TotalInputTokens  int64 `json:"total_input_tokens"`
		TotalOutputTokens int64 `json:"total_output_tokens"`
	} `json:"context_window"`
}

// serverData is what we cache: only the fields the line renders.
type serverData struct {
	TodayTokens int64       `json:"today_tokens"`
	TodayCost   float64     `json:"today_cost"`
	Devices     int         `json:"devices"`
	Quotas      []quotaLine `json:"quotas"`
	FetchedAt   time.Time   `json:"fetched_at"`
}

type quotaLine struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	RemainMin   int     `json:"remain_minutes"`
}

// Run renders one status line. It never returns an error for "server down" —
// that path prints what it can (cached or session-only) and succeeds.
func Run(cfg Config, stdin io.Reader, stdout io.Writer) error {
	cfg.applyDefaults()

	var sess sessionInput
	if stdin != nil {
		if raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &sess) // malformed input must not break the line
		}
	}

	data, stale := loadOrFetch(cfg)
	line := render(cfg, sess, data, stale)
	_, err := fmt.Fprintln(stdout, line)
	return err
}

// loadOrFetch returns server data plus whether it is stale (cache used
// because the live fetch failed).
func loadOrFetch(cfg Config) (*serverData, bool) {
	cached := readCache(cfg.CachePath)
	if cached != nil && time.Since(cached.FetchedAt) < cacheFresh {
		return cached, false
	}
	fresh, err := fetch(cfg)
	if err != nil || fresh == nil {
		return cached, cached != nil
	}
	writeCache(cfg.CachePath, fresh)
	return fresh, false
}

func fetch(cfg Config) (*serverData, error) {
	client := &http.Client{Timeout: fetchTimeout}
	get := func(path string, out any) error {
		req, err := http.NewRequest("GET", cfg.Server+path, nil)
		if err != nil {
			return err
		}
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %s", resp.Status)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}

	var overview struct {
		Today struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"today"`
		Costs map[string]struct {
			RealUSD       float64 `json:"real_usd"`
			EquivalentUSD float64 `json:"equivalent_usd"`
		} `json:"costs"`
		ByDevice []struct {
			Key string `json:"key"`
		} `json:"by_device"`
	}
	if err := get("/api/v1/overview?days=1", &overview); err != nil {
		return nil, err
	}
	var quota struct {
		Quotas []struct {
			Source      string  `json:"source"`
			Scope       string  `json:"scope"`
			UsedPercent float64 `json:"used_percent"`
			ResetsAt    int64   `json:"resets_at"`
			Expired     bool    `json:"expired"`
		} `json:"quotas"`
	}
	_ = get("/api/v1/quota", &quota) // quota is optional; keep the rest of the line

	d := &serverData{
		TodayTokens: overview.Today.TotalTokens,
		Devices:     len(overview.ByDevice),
		FetchedAt:   time.Now(),
	}
	if c, ok := overview.Costs["today"]; ok {
		d.TodayCost = c.RealUSD + c.EquivalentUSD
	}
	now := time.Now().UnixMilli()
	for _, q := range quota.Quotas {
		// Only authoritative primary windows belong on a status line; an
		// expired snapshot is stale state, not a current number.
		if q.Expired || q.ResetsAt == 0 {
			continue
		}
		label := ""
		switch q.Scope {
		case "five_hour", "secondary":
			label = "5h"
		case "seven_day", "primary":
			label = "周"
		default:
			continue
		}
		if q.Source == "codex" {
			label = "cx" + label
		}
		d.Quotas = append(d.Quotas, quotaLine{
			Label:       label,
			UsedPercent: q.UsedPercent,
			RemainMin:   int(max(q.ResetsAt-now, 0) / 60000),
		})
	}
	return d, nil
}

func readCache(path string) *serverData {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var d serverData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil
	}
	return &d
}

func writeCache(path string, d *serverData) {
	data, err := json.Marshal(d)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, path)
	}
}
