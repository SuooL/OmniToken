package server

import (
	"log"
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
	Kind          string  `json:"kind"`                     // subscription | api
	WindowMinutes int     `json:"window_minutes,omitempty"` // 300 | 10080, subscription only
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
	// Which authoritative reading this card absorbed, so the quota row above can
	// drop the snapshot the card already states and stop drawing it twice. The
	// identity is (source, window minutes, scope): one account reports several
	// scopes for one window — `seven_day` beside per-model `seven_day:opus` —
	// and only the tightest becomes a card.
	//
	// Labelling, not filtering: `quotas[]` in the same payload stays whole,
	// because the menubar raises its 75%/90% alerts by walking that list, and a
	// server-side removal would disable the alert for the window on screen.
	Source     string `json:"source,omitempty"`
	Scope      string `json:"scope,omitempty"`
	ObservedAt int64  `json:"observed_at,omitempty"` // when the reading was taken
	// Burn-rate projection (F11): observed rate over the elapsed part of the
	// window, extrapolated to the window end. Only meaningful for a window
	// with a real end — a rolling look-back has none.
	RatePerMin       int64   `json:"rate_per_minute,omitempty"`
	RemainingMinutes int     `json:"remaining_minutes,omitempty"`
	ProjectedTokens  int64   `json:"projected_tokens,omitempty"`
	ProjectedPercent float64 `json:"projected_percent,omitempty"`
	// What the window holds, learned across past windows (ADR-0025). Absent
	// until enough windows have been observed — an unknown allowance is left
	// unstated rather than guessed.
	CapacityTokens  int64 `json:"capacity_tokens,omitempty"`
	RemainingTokens int64 `json:"remaining_tokens,omitempty"`
}

const fiveHours = 5 * time.Hour

// projectionMinElapsed is the fraction of a window that must have gone by
// before its burn rate is extrapolated to the end.
//
// A rate is only worth extrapolating over a span comparable to the one it was
// measured on. Without this the weekly card took a rate measured over 90
// minutes, ran it out over the remaining 166 hours, and announced 980% — a
// number that says nothing except that the window had barely started. The
// five-hour card had the same flaw and merely hid it better, since a fifth of
// it passes in an hour.
const projectionMinElapsed = 0.1

// projectToWindowEnd fills the burn-rate projection (F11): at the rate
// observed so far, how much will this window hold by the time it resets.
// Skipped when the window has no real end (rolling look-back) or when too
// little of it has elapsed for a rate to mean anything.
func (c *windowCard) projectToWindowEnd(now time.Time, start time.Time) {
	if c.ResetsAt == 0 {
		return
	}
	elapsedMin := now.Sub(start).Minutes()
	remainMin := time.UnixMilli(c.ResetsAt).Sub(now).Minutes()
	if elapsedMin < 1 || remainMin <= 0 {
		return
	}
	if windowMin := elapsedMin + remainMin; elapsedMin < windowMin*projectionMinElapsed {
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

// fillCapacity feeds this window's live reading into the calibration and reads
// back what the window is worth (ADR-0025).
//
// Both halves live here because this is the only place that holds a window's
// identity, its authoritative percentage and the tokens this fleet put into it
// at the same time. The write is an upsert that only ever raises a window's
// peak, so repeating it on every snapshot is free; a failure is logged and
// dropped, since a calibration miss must not cost the panel its live view.
func (s *Server) fillCapacity(card *windowCard, source string, w fiveHourWin, windowMinutes int) {
	if err := s.store.ObserveCapacity(store.CapacityObservation{
		Source: source, Scope: w.scope, WindowMinutes: windowMinutes,
		ResetsAt: w.resets.UnixMilli(), UsedPercent: w.pct, Tokens: card.Tokens,
	}); err != nil {
		log.Printf("quota[capacity]: observe: %v", err)
		return
	}
	capacity, ok, err := s.store.CapacityEstimate(source, w.scope, windowMinutes)
	if err != nil {
		log.Printf("quota[capacity]: estimate: %v", err)
		return
	}
	if !ok {
		return
	}
	card.CapacityTokens = capacity
	// A window that has outrun the estimate has nothing left to promise, and
	// saying "-40M remaining" would be worse than saying nothing.
	card.RemainingTokens = max(capacity-card.Tokens, 0)
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
	scope         string
	observed      int64 // unix ms; how fresh the reading is
}

// windowKinds are the quota windows the panel draws a card for. Both are
// subscription-only: a metered channel has no window to be a percentage of
// (ADR-0018 §7).
var windowKinds = []struct {
	minutes int
	label   string
}{
	{300, "5 小时窗口"},
	{10080, "周窗口"},
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
func tightestQuota(quotas []model.QuotaSnapshot, now time.Time, windowMinutes int) map[string]fiveHourWin {
	out := map[string]fiveHourWin{}
	span := time.Duration(windowMinutes) * time.Minute
	for _, q := range quotas {
		if q.WindowMinutes != windowMinutes || q.ResetsAt == 0 || q.ResetsAt < now.UnixMilli() {
			continue
		}
		if cur, ok := out[q.Source]; ok && cur.pct >= q.UsedPercent {
			continue
		}
		resets := time.UnixMilli(q.ResetsAt)
		out[q.Source] = fiveHourWin{
			start: resets.Add(-span), resets: resets, pct: q.UsedPercent,
			scope: q.Scope, observed: q.ObservedAt,
		}
	}
	return out
}

func (s *Server) buildWindowCards(now time.Time, quotas []model.QuotaSnapshot) ([]windowCard, error) {
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
	for _, kind := range windowKinds {
		quota := tightestQuota(quotas, now, kind.minutes)
		for _, src := range []struct{ source, label string }{
			{"claude-code", "Claude Code 订阅"},
			{"codex", "Codex 订阅"},
		} {
			source := src.source
			w, authoritative := quota[source]
			// A weekly card with no weekly quota would be a rolling 7-day
			// look-back nobody asked for; the 5-hour one keeps its placeholder
			// because the page is built around that window.
			if !authoritative && kind.minutes != 300 {
				continue
			}
			keep := func(u store.ProviderUsage) bool {
				return u.Source == source && store.IsSubscription(u.Source, u.Provider)
			}
			// The 5-hour card keeps the bare source as its key: it predates the
			// weekly one and both the panel and the menubar address it by that.
			key := source
			if kind.minutes != 300 {
				key = source + ":weekly"
			}
			card := windowCard{
				Key: key, Kind: "subscription", WindowMinutes: kind.minutes,
				Label: src.label + " · " + kind.label,
				EndMS: now.UnixMilli(),
			}
			from := rollingStart
			if authoritative {
				card.Authoritative = true
				card.StartMS, card.ResetsAt, card.UsedPercent = w.start.UnixMilli(), w.resets.UnixMilli(), w.pct
				card.Source, card.Scope, card.ObservedAt = source, w.scope, w.observed
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
			if authoritative {
				s.fillCapacity(&card, source, w, kind.minutes)
			}
			cards = append(cards, card)
		}
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

// capacityBackfillWindow is how far back the calibration reads on startup.
// Beyond this the events a sample would be derived from are gone anyway —
// Claude Code prunes its logs at 30 days.
const capacityBackfillWindow = 30 * 24 * time.Hour

// backfillCapacity seeds the calibration from history at startup (ADR-0025).
//
// In the background, and never fatal: this only sharpens an estimate that is
// designed to be absent until it has evidence, so failing to run it costs
// nothing that was promised. Doing it synchronously would put a scan of every
// stored quota window in front of the listener.
func (s *Server) backfillCapacity() {
	filled, err := s.store.BackfillCapacity(time.Now().Add(-capacityBackfillWindow))
	if err != nil {
		log.Printf("quota[capacity]: backfill: %v", err)
		return
	}
	if filled > 0 {
		log.Printf("quota[capacity]: 从历史补齐 %d 个窗口样本", filled)
	}
}
