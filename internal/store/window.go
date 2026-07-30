package store

import "time"

// Window usage: how many tokens were spent inside a given time window,
// split by billing channel. Channels bill differently and must never be
// summed into one number (ADR-0005/0007):
//
//   - subscription: Claude Code on the first-party endpoint (OAuth), and
//     Codex — bound by 5h / weekly quota windows;
//   - api: pay-per-use (anthropic-api, bedrock, vertex, openai-api, relays)
//     — no window concept, only a rolling look-back makes sense.
type ChannelUsage struct {
	Source      string  `json:"source"`
	Provider    string  `json:"provider"`
	Tokens      int64   `json:"tokens"`
	OutTokens   int64   `json:"output_tokens"`
	Events      int64   `json:"events"`
	Cache1h     int64   `json:"cache_1h_tokens"`
	Cache5m     int64   `json:"cache_5m_tokens"`
	Input       int64   `json:"input_tokens"`
	CacheRead   int64   `json:"cache_read_tokens"`
	CacheCreate int64   `json:"cache_creation_tokens"`
	Model       string  `json:"model"`
	CostUSD     float64 `json:"cost_usd,omitempty"` // filled by the server layer
}

// UsageByChannel aggregates [from, to) grouped by (source, provider, model)
// so callers can classify and price each slice.
func (s *Store) UsageByChannel(from, to time.Time) ([]ChannelUsage, error) {
	rows, err := s.db.Query(
		`SELECT source, provider, model,
		        COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0),
		        COALESCE(SUM(output_tokens),0), COUNT(*),
		        COALESCE(SUM(input_tokens),0), COALESCE(SUM(cache_read_tokens),0),
		        COALESCE(SUM(cache_creation_tokens),0),
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY source, provider, model`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelUsage
	for rows.Next() {
		var c ChannelUsage
		if err := rows.Scan(&c.Source, &c.Provider, &c.Model, &c.Tokens, &c.OutTokens, &c.Events,
			&c.Input, &c.CacheRead, &c.CacheCreate, &c.Cache1h, &c.Cache5m); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SubscriptionProviders are the channels covered by a subscription quota
// window. Everything else is pay-per-use.
var SubscriptionProviders = map[string]bool{
	"anthropic":       true, // channel not yet probed (F9) — assume subscription
	"anthropic-oauth": true,
	"openai":          true, // Codex on a ChatGPT plan
	"unknown":         true,
}

// IsSubscription reports whether usage falls under a subscription window.
//
// The provider label decides, for every source alike. Proxy traffic used to be
// excluded outright on the grounds that it "always carries an API key" — the
// same wrong assumption the proxy itself made when stamping the label. A
// subscription tool pointed at the proxy forwards its OAuth token untouched,
// and the proxy now says so (agent.proxyProvider), so there is nothing left for
// a special case to decide.
func IsSubscription(source, provider string) bool {
	return SubscriptionProviders[provider]
}
