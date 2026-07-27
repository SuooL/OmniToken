package collect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
)

// Claude subscription quota via Anthropic's OAuth usage endpoint (ADR-0007).
//
// Unlike Codex, Claude Code logs carry no quota state, so the 5h/weekly
// windows could previously only be inferred. This endpoint — the same one
// ccstatusline uses — returns the account's authoritative utilization and
// reset times, which is why status lines built on it are exact.
//
// The access token is read from the local Claude Code credentials (file or
// macOS keychain) and used only as a Bearer header: it is never logged,
// stored, or included in any snapshot.
const (
	usageAPIURL   = "https://api.anthropic.com/api/oauth/usage"
	usageAPIBeta  = "oauth-2025-04-20"
	keychainSvc   = "Claude Code-credentials"
	usageTimeout  = 10 * time.Second
	quotaLimitID  = "claude-oauth-usage"
	defaultBackof = 15 * time.Minute
)

type usageBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type usageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type usageResponse struct {
	FiveHour       *usageBucket `json:"five_hour"`
	SevenDay       *usageBucket `json:"seven_day"`
	SevenDaySonnet *usageBucket `json:"seven_day_sonnet"`
	SevenDayOpus   *usageBucket `json:"seven_day_opus"`
	Limits         []usageLimit `json:"limits"`
}

// claudeAccessToken returns the local OAuth access token, or "" if this
// machine has no subscription credentials (API-key users, servers, …).
func claudeAccessToken() string {
	home, err := os.UserHomeDir()
	if err == nil {
		data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
		if err == nil {
			if tok := parseCredentialToken(data); tok != "" {
				return tok
			}
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password", "-s", keychainSvc, "-w").Output()
		if err == nil {
			if tok := parseCredentialToken(out); tok != "" {
				return tok
			}
		}
	}
	return ""
}

func parseCredentialToken(data []byte) string {
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return ""
	}
	return creds.ClaudeAiOauth.AccessToken
}

// ClaudeQuotaPoller fetches subscription quota on an interval, respecting
// 429 backoff. Zero value is unusable; construct with NewClaudeQuotaPoller.
//
// Quota windows exist only under a subscription. On API-key billing usage is
// pay-per-use with no 5h/weekly window, so polling is skipped entirely — a
// stale OAuth credential left on the machine must not produce quota numbers
// that do not apply to how this machine actually bills.
type ClaudeQuotaPoller struct {
	device      string
	client      *http.Client
	authProbe   func() string
	nextAllowed time.Time
	url         string        // overridden in tests
	tokenFn     func() string // overridden in tests
}

// NewClaudeQuotaPoller takes the F9 auth probe (may be nil). When the probe
// reports API-key billing, Fetch is a no-op.
func NewClaudeQuotaPoller(device string, authProbe func() string) *ClaudeQuotaPoller {
	return &ClaudeQuotaPoller{
		device:    device,
		client:    &http.Client{Timeout: usageTimeout},
		authProbe: authProbe,
		url:       usageAPIURL,
		tokenFn:   claudeAccessToken,
	}
}

// Fetch returns quota snapshots, or nil when there are no credentials, the
// call is backing off, or the endpoint errors (quota is best-effort state).
func (p *ClaudeQuotaPoller) Fetch(now time.Time) ([]model.QuotaSnapshot, error) {
	if now.Before(p.nextAllowed) {
		return nil, nil
	}
	if p.authProbe != nil && p.authProbe() == AuthAnthropicAPI {
		// API-key billing: no subscription window to report.
		return nil, nil
	}
	token := p.tokenFn()
	if token == "" {
		p.nextAllowed = now.Add(time.Hour) // no credentials here; stop retrying hard
		return nil, nil
	}
	req, err := http.NewRequest("GET", p.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", usageAPIBeta)
	resp, err := p.client.Do(req)
	if err != nil {
		p.nextAllowed = now.Add(defaultBackof)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		p.nextAllowed = now.Add(retryAfter(resp.Header.Get("Retry-After"), defaultBackof))
		return nil, fmt.Errorf("usage api rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		p.nextAllowed = now.Add(defaultBackof)
		return nil, fmt.Errorf("usage api status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseUsageResponse(body, p.device, now)
}

func retryAfter(h string, def time.Duration) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return def
	}
	if secs, err := time.ParseDuration(h + "s"); err == nil && secs > 0 {
		return secs
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return def
}

// parseUsageResponse maps the API payload to snapshots. Both shapes are
// handled: flat five_hour/seven_day buckets and the newer limits[] array
// (scope.model.display_name carries per-model weekly quotas).
func parseUsageResponse(body []byte, device string, now time.Time) ([]model.QuotaSnapshot, error) {
	var r usageResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	var out []model.QuotaSnapshot
	add := func(scope string, windowMinutes int, pct *float64, resetsAt *string) {
		if pct == nil && resetsAt == nil {
			return
		}
		var used float64
		if pct != nil {
			used = *pct
		}
		var resetMS int64
		if resetsAt != nil && *resetsAt != "" {
			if t, err := time.Parse(time.RFC3339, *resetsAt); err == nil {
				resetMS = t.UnixMilli()
			}
		}
		// A 0% window with no reset time is a placeholder, not a real window.
		if used == 0 && resetMS == 0 {
			return
		}
		out = append(out, model.QuotaSnapshot{
			Device:        device,
			Source:        claudecode.Source,
			LimitID:       quotaLimitID,
			Scope:         scope,
			WindowMinutes: windowMinutes,
			UsedPercent:   used,
			ResetsAt:      resetMS,
			ObservedAt:    now.UnixMilli(),
		})
	}
	if b := r.FiveHour; b != nil {
		add("five_hour", 300, b.Utilization, b.ResetsAt)
	}
	if b := r.SevenDay; b != nil {
		add("seven_day", 10080, b.Utilization, b.ResetsAt)
	}
	if b := r.SevenDaySonnet; b != nil {
		add("seven_day_sonnet", 10080, b.Utilization, b.ResetsAt)
	}
	if b := r.SevenDayOpus; b != nil {
		add("seven_day_opus", 10080, b.Utilization, b.ResetsAt)
	}
	for _, l := range r.Limits {
		switch l.Kind {
		case "five_hour", "session":
			add("five_hour", 300, l.Percent, l.ResetsAt)
		case "weekly", "seven_day":
			add("seven_day", 10080, l.Percent, l.ResetsAt)
		case "weekly_scoped":
			name := "model"
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
				name = strings.ToLower(l.Scope.Model.DisplayName)
			}
			add("seven_day:"+name, 10080, l.Percent, l.ResetsAt)
		}
	}
	return dedupeScopes(out), nil
}

// dedupeScopes keeps the first snapshot per scope: the flat buckets are the
// primary source and limits[] entries only fill gaps.
func dedupeScopes(in []model.QuotaSnapshot) []model.QuotaSnapshot {
	seen := map[string]bool{}
	var out []model.QuotaSnapshot
	for _, q := range in {
		if seen[q.Scope] {
			continue
		}
		seen[q.Scope] = true
		out = append(out, q)
	}
	return out
}
