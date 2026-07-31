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
type fiveHourWin struct {
	start, resets time.Time
	pct           float64
}

// tightestFiveHourQuota picks, per source, the live 5-hour reading the user is
// closest to exhausting.
//
// Every device signed into one account reports that account's window, and the
// readings differ because each observed at a different moment. Keeping whichever
// row came last would let map iteration order decide how full the panel says the
// window is, and the menubar's quota card leads with that number. The tray
// already resolves this by taking the tightest (`gauge::tightest_percent`), so
// any other rule here also makes the two disagree about the same account.
func tightestFiveHourQuota(quotas []model.QuotaSnapshot, now time.Time) map[string]fiveHourWin {
	out := map[string]fiveHourWin{}
	for _, q := range quotas {
		if q.WindowMinutes != 300 || q.ResetsAt == 0 || q.ResetsAt < now.UnixMilli() {
			continue
		}
		if cur, ok := out[q.Source]; ok && cur.pct >= q.UsedPercent {
			continue
		}
		resets := time.UnixMilli(q.ResetsAt)
		out[q.Source] = fiveHourWin{start: resets.Add(-fiveHours), resets: resets, pct: q.UsedPercent}
	}
	return out
}

func (s *Server) buildWindowCards(now time.Time, quotas []model.QuotaSnapshot) ([]windowCard, error) {
	fiveHourQuota := tightestFiveHourQuota(quotas, now)
	rollingStart := now.Add(-fiveHours)

	sum := func(from time.Time, keep func(store.ProviderUsage) bool) (tokens, events int64, cost float64, err error) {
		rows, err := s.store.UsageByProvider(from, now.Add(time.Minute))
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
		keep := func(u store.ProviderUsage) bool {
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

	// Everything that is not a subscription, one card per channel (ADR-0018).
	// These used to share a single "API 计费" card defined as "not
	// subscription", which put relay spend under the official-API heading and
	// swept the unclassified remainder in with it — three different billing
	// relationships presented as one number.
	//
	// None of them gets a quota bar or a projection: a metered channel has no
	// window to be a percentage of, so drawing one would be a category error
	// rather than a missing feature (ADR-0018 §7). A channel with no usage in
	// the look-back produces no card at all.
	for _, ch := range []struct{ channel, label, note string }{
		{model.ChannelAPI, "官方 API · 最近 5 小时", "第一方按量计费,无配额窗口,按最近 5 小时滚动统计"},
		{model.ChannelRelay, "第三方中转 · 最近 5 小时", "非第一方端点,不占用订阅额度;成本按公开价推算,仅供参考"},
		{model.ChannelUnknown, "未知通道 · 最近 5 小时", "证据不足以判定计费通道,既不计入订阅也不并入任何计费类"},
	} {
		channel := ch.channel
		tokens, events, cost, err := sum(rollingStart, func(u store.ProviderUsage) bool {
			return model.BillingChannel(u.Provider) == channel
		})
		if err != nil {
			return nil, err
		}
		if tokens == 0 {
			continue
		}
		cards = append(cards, windowCard{
			Key: channel, Kind: channel, Label: ch.label,
			Tokens: tokens, Events: events, CostUSD: cost,
			StartMS: rollingStart.UnixMilli(), EndMS: now.UnixMilli(),
			Note: ch.note,
		})
	}
	return cards, nil
}
