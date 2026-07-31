package server

import (
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

// PeriodCost splits spend by billing reality (ADR-0005), along the channel
// boundary ADR-0018 draws: metered channels (official API and third-party
// relays) cost real dollars, a subscription costs "equivalent" value against a
// flat fee already paid.
//
// UnknownUSD is the third bucket and it is not padding. Rolling unclassified
// spend into either of the other two would make that number a guess wearing a
// precise label — and since it is `unknown` precisely because the evidence is
// missing, there is no direction in which the guess would be safe. It is
// reported separately so the panel can show how much of the total is not yet
// attributable to a channel.
type PeriodCost struct {
	RealUSD       float64 `json:"real_usd"`
	EquivalentUSD float64 `json:"equivalent_usd"`
	UnknownUSD    float64 `json:"unknown_usd"`
}

func (s *Server) costFromUsage(rows []store.ModelUsageRow) (PeriodCost, map[string]float64, []string) {
	var pc PeriodCost
	perModel := map[string]float64{}
	unpricedSet := map[string]bool{}
	for _, r := range rows {
		cost, ok := s.Prices().Cost(r.Model, time.UnixMilli(r.MinTS),
			r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheCreation, r.Cache1h, r.Cache5m)
		if !ok {
			// Unpriced ids stay exactly as reported: this list is what the user
			// pastes into pricing_overrides, and a folded display name would not
			// match the events it is meant to price.
			unpricedSet[r.Model] = true
			continue
		}
		// Priced by the reported id, reported under the display name — two
		// routing variants of one model are one line of spend, not two.
		perModel[model.CanonicalModel(r.Model)] += cost
		switch model.BillingChannel(r.Provider) {
		case model.ChannelAPI, model.ChannelRelay:
			pc.RealUSD += cost
		case model.ChannelSubscription:
			pc.EquivalentUSD += cost
		default:
			pc.UnknownUSD += cost
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
