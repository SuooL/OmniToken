package server

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

// Antigravity quota groups (ADR-0044).
//
// Antigravity accounts meter three official model pools on rolling 5-hour and
// weekly limits, but Antigravity does not persist official quota percentages or
// reset timestamps to local disk. OmniToken therefore aggregates rolling 5h and
// 7d event tokens across those three pools and keeps them strictly separate
// from authoritative `quotas[]` / `windows[]` readings.

const (
	AntigravityGroupGeminiPro   = "gemini-pro"
	AntigravityGroupGeminiFlash = "gemini-flash"
	AntigravityGroupThirdParty  = "third-party"

	settingsKeyAntigravityBudgets = "antigravity_quota_budgets"
	maxGroupBudgetTokens          = int64(100_000_000_000_000) // 100T tokens sanity bound
)

type antigravityGroupDef struct {
	ID    string
	Label string
}

var antigravityGroupOrder = []antigravityGroupDef{
	{ID: AntigravityGroupGeminiPro, Label: "Gemini Pro / 4"},
	{ID: AntigravityGroupGeminiFlash, Label: "Gemini Flash"},
	{ID: AntigravityGroupThirdParty, Label: "Claude / 第三方"},
}

var validAntigravityGroups = map[string]bool{
	AntigravityGroupGeminiPro:   true,
	AntigravityGroupGeminiFlash: true,
	AntigravityGroupThirdParty:  true,
}

// AntigravityGroupBudget holds optional user-configured reference token caps
// for one Antigravity model pool (ADR-0044). Zero means unconfigured.
type AntigravityGroupBudget struct {
	FiveHourTokens int64 `json:"five_hour_tokens,omitempty"`
	WeeklyTokens   int64 `json:"weekly_tokens,omitempty"`
}

// QuotaGroupModelUsage is one concrete model's rolling 5h and 7d usage inside a
// quota group.
type QuotaGroupModelUsage struct {
	Model                       string   `json:"model"`
	FiveHourTokens              int64    `json:"five_hour_tokens"`
	WeeklyTokens                int64    `json:"weekly_tokens"`
	FiveHourInputTokens         int64    `json:"five_hour_input_tokens"`
	FiveHourOutputTokens        int64    `json:"five_hour_output_tokens"`
	FiveHourCacheReadTokens     int64    `json:"five_hour_cache_read_tokens"`
	FiveHourCacheCreationTokens int64    `json:"five_hour_cache_creation_tokens"`
	WeeklyInputTokens           int64    `json:"weekly_input_tokens"`
	WeeklyOutputTokens          int64    `json:"weekly_output_tokens"`
	WeeklyCacheReadTokens       int64    `json:"weekly_cache_read_tokens"`
	WeeklyCacheCreationTokens   int64    `json:"weekly_cache_creation_tokens"`
	FiveHourEvents              int64    `json:"five_hour_events"`
	WeeklyEvents                int64    `json:"weekly_events"`
	FiveHourCostUSD             *float64 `json:"five_hour_cost_usd,omitempty"`
	WeeklyCostUSD               *float64 `json:"weekly_cost_usd,omitempty"`
}

// QuotaGroupEntry is one official model pool inside a grouped rolling summary.
type QuotaGroupEntry struct {
	ID                      string                 `json:"id"`
	Label                   string                 `json:"label"`
	FiveHourTokens          int64                  `json:"five_hour_tokens"`
	WeeklyTokens            int64                  `json:"weekly_tokens"`
	FiveHourSharePercent    *float64               `json:"five_hour_share_percent,omitempty"`
	WeeklySharePercent      *float64               `json:"weekly_share_percent,omitempty"`
	FiveHourEvents          int64                  `json:"five_hour_events"`
	WeeklyEvents            int64                  `json:"weekly_events"`
	FiveHourCostUSD         *float64               `json:"five_hour_cost_usd,omitempty"`
	WeeklyCostUSD           *float64               `json:"weekly_cost_usd,omitempty"`
	FiveHourBudgetTokens    int64                  `json:"five_hour_budget_tokens,omitempty"`
	WeeklyBudgetTokens      int64                  `json:"weekly_budget_tokens,omitempty"`
	FiveHourRemainingTokens *int64                 `json:"five_hour_remaining_tokens,omitempty"`
	WeeklyRemainingTokens   *int64                 `json:"weekly_remaining_tokens,omitempty"`
	FiveHourUsedPercent     *float64               `json:"five_hour_used_percent,omitempty"`
	WeeklyUsedPercent       *float64               `json:"weekly_used_percent,omitempty"`
	Models                  []QuotaGroupModelUsage `json:"models"`
}

// QuotaGroupSummary is a source-level rolling estimate summary emitted under
// `/api/v1/live` `quota_groups[]`.
type QuotaGroupSummary struct {
	Source                     string            `json:"source"`
	Label                      string            `json:"label"`
	Basis                      string            `json:"basis"` // "rolling_estimate"
	FiveHourTokens             int64             `json:"five_hour_tokens"`
	WeeklyTokens               int64             `json:"weekly_tokens"`
	FiveHourBudgetTokens       int64             `json:"five_hour_budget_tokens,omitempty"`
	WeeklyBudgetTokens         int64             `json:"weekly_budget_tokens,omitempty"`
	FiveHourRemainingTokens    *int64            `json:"five_hour_remaining_tokens,omitempty"`
	WeeklyRemainingTokens      *int64            `json:"weekly_remaining_tokens,omitempty"`
	FiveHourUsedPercent        *float64          `json:"five_hour_used_percent,omitempty"`
	WeeklyUsedPercent          *float64          `json:"weekly_used_percent,omitempty"`
	BudgetConfiguredGroups     int               `json:"budget_configured_groups"`
	UnclassifiedFiveHourTokens int64             `json:"unclassified_five_hour_tokens,omitempty"`
	UnclassifiedWeeklyTokens   int64             `json:"unclassified_weekly_tokens,omitempty"`
	Groups                     []QuotaGroupEntry `json:"groups"`
}

// ClassifyAntigravityModel maps a raw model ID to one of Antigravity's three
// official quota pools. Empty model IDs return "" rather than guessing a pool.
func ClassifyAntigravityModel(raw string) string {
	m := strings.ToLower(strings.TrimSpace(raw))
	for _, prefix := range []string{
		"vertex_ai/",
		"vertex/",
		"google/",
		"anthropic/",
		"openai/",
		"models/",
	} {
		if strings.HasPrefix(m, prefix) {
			m = strings.TrimPrefix(m, prefix)
			break
		}
	}
	m = strings.TrimSpace(m)
	if m == "" {
		return ""
	}
	if strings.HasPrefix(m, "gemini") {
		if strings.Contains(m, "flash") {
			return AntigravityGroupGeminiFlash
		}
		return AntigravityGroupGeminiPro
	}
	return AntigravityGroupThirdParty
}

func validateAntigravityBudgets(in map[string]AntigravityGroupBudget) (map[string]AntigravityGroupBudget, error) {
	out := make(map[string]AntigravityGroupBudget, len(in))
	for rawKey, b := range in {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if !validAntigravityGroups[key] {
			return nil, fmt.Errorf("Antigravity 分组预算:不支持的分组 %q(仅支持 gemini-pro、gemini-flash、third-party)", rawKey)
		}
		if b.FiveHourTokens < 0 || b.FiveHourTokens > maxGroupBudgetTokens {
			return nil, fmt.Errorf("Antigravity 分组预算:%s 的 5h 上限 %d 非法(须在 0 到 %d 之间)", key, b.FiveHourTokens, maxGroupBudgetTokens)
		}
		if b.WeeklyTokens < 0 || b.WeeklyTokens > maxGroupBudgetTokens {
			return nil, fmt.Errorf("Antigravity 分组预算:%s 的周上限 %d 非法(须在 0 到 %d 之间)", key, b.WeeklyTokens, maxGroupBudgetTokens)
		}
		if b.FiveHourTokens == 0 && b.WeeklyTokens == 0 {
			continue
		}
		out[key] = b
	}
	return out, nil
}

// effectiveAntigravityBudgets merges config.json budgets with DB-stored
// settings overrides (DB wins per group key, matching pricing_overrides).
func (s *Server) effectiveAntigravityBudgets() (map[string]AntigravityGroupBudget, error) {
	merged := map[string]AntigravityGroupBudget{}
	if s.cfg != nil {
		for k, v := range s.cfg.AntigravityQuotaBudgets {
			key := strings.ToLower(strings.TrimSpace(k))
			if validAntigravityGroups[key] && (v.FiveHourTokens > 0 || v.WeeklyTokens > 0) {
				merged[key] = v
			}
		}
	}
	var stored map[string]AntigravityGroupBudget
	if err := s.store.GetSettingsJSON(settingsKeyAntigravityBudgets, &stored); err != nil {
		return nil, err
	}
	for k, v := range stored {
		key := strings.ToLower(strings.TrimSpace(k))
		if !validAntigravityGroups[key] {
			continue
		}
		if v.FiveHourTokens == 0 && v.WeeklyTokens == 0 {
			delete(merged, key)
			continue
		}
		merged[key] = v
	}
	return merged, nil
}

func (s *Server) buildQuotaGroups(now time.Time) ([]QuotaGroupSummary, error) {
	fiveHourRows, err := s.store.SourceModelUsage("antigravity", now.Add(-5*time.Hour), now)
	if err != nil {
		return nil, err
	}
	weeklyRows, err := s.store.SourceModelUsage("antigravity", now.Add(-7*24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	budgets, err := s.effectiveAntigravityBudgets()
	if err != nil {
		return nil, err
	}
	summary := buildAntigravityQuotaGroupSummary(now, fiveHourRows, weeklyRows, budgets, s.Prices())
	return []QuotaGroupSummary{summary}, nil
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func priceProviderUsage(at time.Time, u store.ProviderUsage, prices *pricing.Table) (float64, bool) {
	if prices == nil {
		return 0, false
	}
	return prices.Cost(u.Model, at, u.Input, u.OutTokens, u.CacheRead, u.CacheCreate, u.Cache1h, u.Cache5m)
}

func buildAntigravityQuotaGroupSummary(
	at time.Time,
	fiveHourRows, weeklyRows []store.ProviderUsage,
	budgets map[string]AntigravityGroupBudget,
	prices *pricing.Table,
) QuotaGroupSummary {
	type modelAcc struct {
		usage        QuotaGroupModelUsage
		fiveCost     float64
		fivePriced   bool
		weeklyCost   float64
		weeklyPriced bool
	}
	type groupAcc struct {
		entry        QuotaGroupEntry
		models       map[string]*modelAcc
		fiveCost     float64
		fivePriced   bool
		weeklyCost   float64
		weeklyPriced bool
	}

	groupsByID := make(map[string]*groupAcc, len(antigravityGroupOrder))
	for _, def := range antigravityGroupOrder {
		groupsByID[def.ID] = &groupAcc{
			entry: QuotaGroupEntry{
				ID:     def.ID,
				Label:  def.Label,
				Models: []QuotaGroupModelUsage{},
			},
			models: map[string]*modelAcc{},
		}
	}

	summary := QuotaGroupSummary{
		Source: "antigravity",
		Label:  "Antigravity",
		Basis:  "rolling_estimate",
	}

	var classifiedFiveHour, classifiedWeekly int64

	absorb := func(rows []store.ProviderUsage, isFiveHour bool) {
		for _, u := range rows {
			if isFiveHour {
				summary.FiveHourTokens += u.Tokens
			} else {
				summary.WeeklyTokens += u.Tokens
			}
			gid := ClassifyAntigravityModel(u.Model)
			if gid == "" {
				if isFiveHour {
					summary.UnclassifiedFiveHourTokens += u.Tokens
				} else {
					summary.UnclassifiedWeeklyTokens += u.Tokens
				}
				continue
			}
			g := groupsByID[gid]
			cost, ok := priceProviderUsage(at, u, prices)
			if isFiveHour {
				classifiedFiveHour += u.Tokens
				g.entry.FiveHourTokens += u.Tokens
				g.entry.FiveHourEvents += u.Events
				if ok {
					g.fiveCost += cost
					g.fivePriced = true
				}
			} else {
				classifiedWeekly += u.Tokens
				g.entry.WeeklyTokens += u.Tokens
				g.entry.WeeklyEvents += u.Events
				if ok {
					g.weeklyCost += cost
					g.weeklyPriced = true
				}
			}
			modelKey := strings.TrimSpace(u.Model)
			ma := g.models[modelKey]
			if ma == nil {
				ma = &modelAcc{usage: QuotaGroupModelUsage{Model: modelKey}}
				g.models[modelKey] = ma
			}
			if isFiveHour {
				ma.usage.FiveHourTokens += u.Tokens
				ma.usage.FiveHourInputTokens += u.Input
				ma.usage.FiveHourOutputTokens += u.OutTokens
				ma.usage.FiveHourCacheReadTokens += u.CacheRead
				ma.usage.FiveHourCacheCreationTokens += u.CacheCreate
				ma.usage.FiveHourEvents += u.Events
				if ok {
					ma.fiveCost += cost
					ma.fivePriced = true
				}
			} else {
				ma.usage.WeeklyTokens += u.Tokens
				ma.usage.WeeklyInputTokens += u.Input
				ma.usage.WeeklyOutputTokens += u.OutTokens
				ma.usage.WeeklyCacheReadTokens += u.CacheRead
				ma.usage.WeeklyCacheCreationTokens += u.CacheCreate
				ma.usage.WeeklyEvents += u.Events
				if ok {
					ma.weeklyCost += cost
					ma.weeklyPriced = true
				}
			}
		}
	}

	absorb(fiveHourRows, true)
	absorb(weeklyRows, false)

	var (
		hasFiveBudget      bool
		hasWeeklyBudget    bool
		fiveBudgetTotal    int64
		weeklyBudgetTotal  int64
		fiveBudgetUsed     int64
		weeklyBudgetUsed   int64
		fiveRemainTotal    int64
		weeklyRemainTotal  int64
		configuredGroupCnt int
	)

	summary.Groups = make([]QuotaGroupEntry, 0, len(antigravityGroupOrder))
	for _, def := range antigravityGroupOrder {
		g := groupsByID[def.ID]
		if classifiedFiveHour > 0 {
			pct := round1(float64(g.entry.FiveHourTokens) / float64(classifiedFiveHour) * 100)
			g.entry.FiveHourSharePercent = &pct
		}
		if classifiedWeekly > 0 {
			pct := round1(float64(g.entry.WeeklyTokens) / float64(classifiedWeekly) * 100)
			g.entry.WeeklySharePercent = &pct
		}
		if g.fivePriced {
			c := g.fiveCost
			g.entry.FiveHourCostUSD = &c
		}
		if g.weeklyPriced {
			c := g.weeklyCost
			g.entry.WeeklyCostUSD = &c
		}

		b := budgets[def.ID]
		if b.FiveHourTokens > 0 || b.WeeklyTokens > 0 {
			configuredGroupCnt++
		}
		if b.WeeklyTokens > 0 {
			hasWeeklyBudget = true
			g.entry.WeeklyBudgetTokens = b.WeeklyTokens
			rem := max(b.WeeklyTokens-g.entry.WeeklyTokens, 0)
			g.entry.WeeklyRemainingTokens = &rem
			pct := round1(float64(g.entry.WeeklyTokens) / float64(b.WeeklyTokens) * 100)
			g.entry.WeeklyUsedPercent = &pct
			weeklyBudgetTotal += b.WeeklyTokens
			weeklyBudgetUsed += g.entry.WeeklyTokens
			weeklyRemainTotal += rem
		}
		if b.FiveHourTokens > 0 {
			hasFiveBudget = true
			g.entry.FiveHourBudgetTokens = b.FiveHourTokens
			rem := max(b.FiveHourTokens-g.entry.FiveHourTokens, 0)
			if g.entry.WeeklyRemainingTokens != nil && *g.entry.WeeklyRemainingTokens < rem {
				rem = *g.entry.WeeklyRemainingTokens
			}
			g.entry.FiveHourRemainingTokens = &rem
			pct := round1(float64(g.entry.FiveHourTokens) / float64(b.FiveHourTokens) * 100)
			g.entry.FiveHourUsedPercent = &pct
			fiveBudgetTotal += b.FiveHourTokens
			fiveBudgetUsed += g.entry.FiveHourTokens
			fiveRemainTotal += rem
		}

		models := make([]QuotaGroupModelUsage, 0, len(g.models))
		for _, ma := range g.models {
			if ma.fivePriced {
				c := ma.fiveCost
				ma.usage.FiveHourCostUSD = &c
			}
			if ma.weeklyPriced {
				c := ma.weeklyCost
				ma.usage.WeeklyCostUSD = &c
			}
			models = append(models, ma.usage)
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].FiveHourTokens != models[j].FiveHourTokens {
				return models[i].FiveHourTokens > models[j].FiveHourTokens
			}
			if models[i].WeeklyTokens != models[j].WeeklyTokens {
				return models[i].WeeklyTokens > models[j].WeeklyTokens
			}
			return models[i].Model < models[j].Model
		})
		g.entry.Models = models
		summary.Groups = append(summary.Groups, g.entry)
	}

	summary.BudgetConfiguredGroups = configuredGroupCnt
	if hasFiveBudget && fiveBudgetTotal > 0 {
		summary.FiveHourBudgetTokens = fiveBudgetTotal
		rem := fiveRemainTotal
		summary.FiveHourRemainingTokens = &rem
		pct := round1(float64(fiveBudgetUsed) / float64(fiveBudgetTotal) * 100)
		summary.FiveHourUsedPercent = &pct
	}
	if hasWeeklyBudget && weeklyBudgetTotal > 0 {
		summary.WeeklyBudgetTokens = weeklyBudgetTotal
		rem := weeklyRemainTotal
		summary.WeeklyRemainingTokens = &rem
		pct := round1(float64(weeklyBudgetUsed) / float64(weeklyBudgetTotal) * 100)
		summary.WeeklyUsedPercent = &pct
	}
	return summary
}
