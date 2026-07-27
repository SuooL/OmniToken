package server

import (
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// PeriodCost splits spend by billing reality (ADR-0005): API-priced channels
// (bedrock / vertex / relay) are real dollars; subscription-backed channels
// (anthropic OAuth-undetermined, openai/codex) are "equivalent" value until
// F9 auth probing can tell API-key usage apart on the first-party endpoint.
type PeriodCost struct {
	RealUSD       float64 `json:"real_usd"`
	EquivalentUSD float64 `json:"equivalent_usd"`
}

var realProviders = map[string]bool{"bedrock": true, "vertex": true, "relay": true, "anthropic-api": true}

func (s *Server) costFromUsage(rows []store.ModelUsageRow) (PeriodCost, map[string]float64, []string) {
	var pc PeriodCost
	perModel := map[string]float64{}
	unpricedSet := map[string]bool{}
	for _, r := range rows {
		cost, ok := s.Prices().Cost(r.Model, time.UnixMilli(r.MinTS),
			r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheCreation, r.Cache1h, r.Cache5m)
		if !ok {
			unpricedSet[r.Model] = true
			continue
		}
		perModel[r.Model] += cost
		if realProviders[r.Provider] {
			pc.RealUSD += cost
		} else {
			pc.EquivalentUSD += cost
		}
	}
	unpriced := make([]string, 0, len(unpricedSet))
	for m := range unpricedSet {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)
	return pc, perModel, unpriced
}

func (s *Server) periodCost(from, to time.Time) (PeriodCost, error) {
	rows, err := s.store.ModelUsage(from, to)
	if err != nil {
		return PeriodCost{}, err
	}
	pc, _, _ := s.costFromUsage(rows)
	return pc, nil
}
