package server

import (
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

// windowCard is one row of the live page's window section. Each card states
// exactly which traffic it covers and whether its boundary is authoritative
// (from the provider) or a rolling look-back.
type windowCard struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Kind          string  `json:"kind"` // subscription | api
	Tokens        int64   `json:"tokens"`
	Events        int64   `json:"events"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
	UsedPercent   float64 `json:"used_percent,omitempty"` // authoritative quota, if any
	StartMS       int64   `json:"start_ms"`
	EndMS         int64   `json:"end_ms"`
	ResetsAt      int64   `json:"resets_at,omitempty"`
	Authoritative bool    `json:"authoritative"` // window boundary from the provider
	Placeholder   bool    `json:"placeholder"`   // provider reports no such window (yet)
	Note          string  `json:"note,omitempty"`
	// Burn-rate projection (F11): observed rate over the elapsed part of the
	// window, extrapolated to the window end. Only meaningful for a window
	// with a real end — a rolling look-back has none.
	RatePerMin       int64   `json:"rate_per_minute,omitempty"`
	RemainingMinutes int     `json:"remaining_minutes,omitempty"`
	ProjectedTokens  int64   `json:"projected_tokens,omitempty"`
	ProjectedPercent float64 `json:"projected_percent,omitempty"`
}

const fiveHours = 5 * time.Hour

// projectToWindowEnd fills the burn-rate projection (F11): at the rate
// observed so far, how much will this window hold by the time it resets.
// Skipped when the window has no real end (rolling look-back) or when too
// little time has elapsed for a rate to mean anything.
func (c *windowCard) projectToWindowEnd(now time.Time, start time.Time) {
	if c.ResetsAt == 0 {
		return
	}
	elapsedMin := now.Sub(start).Minutes()
	remainMin := time.UnixMilli(c.ResetsAt).Sub(now).Minutes()
	if elapsedMin < 1 || remainMin <= 0 {
		return
	}
	rate := float64(c.Tokens) / elapsedMin
	c.RatePerMin = int64(rate)
	c.RemainingMinutes = int(remainMin)
	c.ProjectedTokens = c.Tokens + int64(rate*remainMin)
	if c.UsedPercent > 0 && c.Tokens > 0 {
		// Scale the authoritative percentage by the same growth factor, so the
		// projection is expressed in the units the quota is actually measured in.
		c.ProjectedPercent = c.UsedPercent * float64(c.ProjectedTokens) / float64(c.Tokens)
	}
}

// buildWindowCards assembles the 5-hour view: one card per subscription
// source (Claude Code, Codex) plus one aggregated API-billing card.
//
// Window boundaries: when the provider told us when the window resets
// (ADR-0007) the count runs over the REAL window [resets_at-5h, now];
// otherwise it falls back to a rolling [now-5h, now] and says so. The two
// billing kinds are never summed — a subscription window and pay-per-use
// usage answer different questions (ADR-0005).
func (s *Server) buildWindowCards(now time.Time, quotas []model.QuotaSnapshot) ([]windowCard, error) {
	type win struct {
		start, resets time.Time
		pct           float64
	}
	fiveHourQuota := map[string]win{}
	for _, q := range quotas {
		if q.WindowMinutes != 300 || q.ResetsAt == 0 || q.ResetsAt < now.UnixMilli() {
			continue
		}
		resets := time.UnixMilli(q.ResetsAt)
		fiveHourQuota[q.Source] = win{start: resets.Add(-fiveHours), resets: resets, pct: q.UsedPercent}
	}
	rollingStart := now.Add(-fiveHours)

	sum := func(from time.Time, keep func(store.ChannelUsage) bool) (tokens, events int64, cost float64, err error) {
		rows, err := s.store.UsageByChannel(from, now.Add(time.Minute))
		if err != nil {
			return 0, 0, 0, err
		}
		for _, u := range rows {
			if !keep(u) {
				continue
			}
			tokens += u.Tokens
			events += u.Events
			if c, ok := s.Prices().Cost(u.Model, now, u.Input, u.OutTokens, u.CacheRead, u.CacheCreate, u.Cache1h, u.Cache5m); ok {
				cost += c
			}
		}
		return tokens, events, cost, nil
	}

	var cards []windowCard
	for _, src := range []struct{ source, label string }{
		{"claude-code", "Claude Code 订阅"},
		{"codex", "Codex 订阅"},
	} {
		source := src.source
		keep := func(u store.ChannelUsage) bool {
			return u.Source == source && store.IsSubscription(u.Source, u.Provider)
		}
		card := windowCard{
			Key: source, Kind: "subscription",
			Label: src.label + " · 5 小时窗口",
			EndMS: now.UnixMilli(),
		}
		from := rollingStart
		if w, ok := fiveHourQuota[source]; ok {
			card.Authoritative = true
			card.StartMS, card.ResetsAt, card.UsedPercent = w.start.UnixMilli(), w.resets.UnixMilli(), w.pct
			from = w.start
		} else {
			card.StartMS = rollingStart.UnixMilli()
			card.Placeholder = true
			card.Note = "该来源当前未提供 5 小时配额数据,此处按最近 5 小时滚动统计"
		}
		var err error
		if card.Tokens, card.Events, card.CostUSD, err = sum(from, keep); err != nil {
			return nil, err
		}
		card.projectToWindowEnd(now, from)
		cards = append(cards, card)
	}

	// Pay-per-use has no window, so a rolling look-back is the only
	// meaningful figure; the card appears only when there is actual usage.
	apiTokens, apiEvents, apiCost, err := sum(rollingStart, func(u store.ChannelUsage) bool {
		return !store.IsSubscription(u.Source, u.Provider)
	})
	if err != nil {
		return nil, err
	}
	if apiTokens > 0 {
		cards = append(cards, windowCard{
			Key: "api", Kind: "api", Label: "API 计费 · 最近 5 小时",
			Tokens: apiTokens, Events: apiEvents, CostUSD: apiCost,
			StartMS: rollingStart.UnixMilli(), EndMS: now.UnixMilli(),
			Note: "按量付费无配额窗口,按最近 5 小时滚动统计",
		})
	}
	return cards, nil
}
