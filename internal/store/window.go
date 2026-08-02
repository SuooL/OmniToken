package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Window usage: how many tokens were spent inside a given time window, grouped
// by the evidence stored on the event. Callers map it onto a billing channel
// with model.BillingChannel; the channels bill differently and must never be
// summed into one number (ADR-0005/0007/0018).
type ProviderUsage struct {
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

// UsageByProvider aggregates [from, to) grouped by (source, provider, model)
// so callers can classify and price each slice.
func (s *Store) UsageByProvider(from, to time.Time) ([]ProviderUsage, error) {
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
	var out []ProviderUsage
	for rows.Next() {
		var c ProviderUsage
		if err := rows.Scan(&c.Source, &c.Provider, &c.Model, &c.Tokens, &c.OutTokens, &c.Events,
			&c.Input, &c.CacheRead, &c.CacheCreate, &c.Cache1h, &c.Cache5m); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IsSubscription reports whether usage falls under a subscription quota window.
//
// The provider label decides, for every source alike. Proxy traffic used to be
// excluded outright on the grounds that it "always carries an API key" — the
// same wrong assumption the proxy itself made when stamping the label. A
// subscription tool pointed at the proxy forwards its OAuth token untouched,
// and the proxy now says so (proxy.proxyProvider), so there is nothing left for
// a special case to decide.
//
// The map this used to consult counted `anthropic` and `unknown` as
// subscription — "not probed yet, assume the common case". That assumption is
// what put relay traffic inside the 5h quota bar and made the two numbers
// beside it contradict each other, so ADR-0018 §7 removes it: only established
// subscription evidence counts, and everything unproven sits in its own column.
func IsSubscription(source, provider string) bool {
	return model.BillingChannel(provider) == model.ChannelSubscription
}

// ChannelRow is one billing channel's slice of a period (ADR-0018 §2). The
// channel is derived from the stored provider at query time, so changing the
// mapping never requires touching a stored row.
type ChannelRow struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	Totals
}

// ChannelBreakdown splits [from, to) across the four billing channels.
//
// Every channel is returned even when empty, `unknown` included. That is the
// point: the panel has to be able to show what share of the data could not be
// classified, and a column that disappears when it is inconvenient is how the
// unclassified usage got quietly folded into the subscription total in the
// first place. The rows partition the period — each event lands in exactly one
// channel — so the four Events counts add up to the period's event count.
func (s *Store) ChannelBreakdown(from, to time.Time) ([]ChannelRow, error) {
	rows, err := s.db.Query(
		`SELECT provider, `+sums+`
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY provider`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byChannel := map[string]*ChannelRow{}
	for _, ch := range model.Channels() {
		byChannel[ch] = &ChannelRow{Channel: ch, Label: model.ChannelLabel(ch)}
	}
	for rows.Next() {
		var provider string
		var t Totals
		if err := rows.Scan(&provider, &t.Events, &t.InputTokens, &t.OutputTokens,
			&t.CacheRead, &t.CacheCreation, &t.TotalTokens); err != nil {
			return nil, err
		}
		c := byChannel[model.BillingChannel(provider)]
		c.Events += t.Events
		c.InputTokens += t.InputTokens
		c.OutputTokens += t.OutputTokens
		c.CacheRead += t.CacheRead
		c.CacheCreation += t.CacheCreation
		c.TotalTokens += t.TotalTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ChannelRow, 0, len(byChannel))
	for _, ch := range model.Channels() {
		out = append(out, *byChannel[ch])
	}
	return out, nil
}

// providerRankSQL mirrors model.ProviderRank as a SQL expression over the
// provider column, so the overwrite guard can be a single statement rather than
// a read-compare-write race. It is built from the model package's own list;
// TestProviderRankSQLMatchesModel keeps the two from drifting apart.
var providerRankSQL = buildProviderRankSQL()

func buildProviderRankSQL() string {
	var b strings.Builder
	b.WriteString("CASE provider")
	for _, p := range model.RankedProviders() {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", p, model.ProviderRank(p))
	}
	fmt.Fprintf(&b, " ELSE %d END", model.DefaultProviderRank())
	return b.String()
}
