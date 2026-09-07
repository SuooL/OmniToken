package store

import (
	"github.com/suool/omnitoken/internal/model"
	"sort"
	"time"
)

// Model page queries (F22 / GAP-4). Two shapes feed the page:
// composition by tool source (which client produced the tokens) and the
// per-day model mix. Costs are never stored — rows carry MinTS so the caller
// can run pricing.Resolve/Cost at query time (ADR-0005).

// ModelOther is the bucket ModelDaily folds every model outside the top N
// into, so a long tail cannot smear the daily chart into unreadable slivers.
const ModelOther = "其他"

// defaultModelTopN is used when the caller passes topN <= 0.
const defaultModelTopN = 6

// ModelSourceRow is one (model, source) aggregate: the four token components
// plus the cache-TTL split and MinTS that cost computation needs.
type ModelSourceRow struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	Totals
	Cache1h int64 `json:"cache_1h_tokens"`
	Cache5m int64 `json:"cache_5m_tokens"`
	MinTS   int64 `json:"min_ts"`
}

// ModelDailyRow is one (local calendar day, model) aggregate.
type ModelDailyRow struct {
	Bucket string `json:"bucket"` // e.g. "2026-07-25"
	Model  string `json:"model"`
	Totals
}

// ModelBySource aggregates usage per (model, source) over [from, to).
//
// Model carries the id the tool reported, NOT the display name: the caller
// prices it, and a folded name is not guaranteed to exist in the pricing table
// (internal/model/canonical.go, "fold when displaying, never before a price
// lookup"). Folding here priced `anthropic.claude-sonnet-4-20250514` as
// `claude-sonnet-4`, which LiteLLM does not carry, so a real model's spend was
// dropped and it was reported as unpriced.
//
// Rows are nonetheless ORDERED by the folded name — models by their folded
// total tokens descending, then sources within a model, then variants — so the
// caller can fold and merge in one forward pass and the stacked bars stay
// grouped.
func (s *Store) ModelBySource(from, to time.Time) ([]ModelSourceRow, error) {
	rows, err := s.rdb.Query(
		`SELECT model, source, `+sums+`,
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0),
		        COALESCE(MIN(ts),0)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY model, source`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelSourceRow
	for rows.Next() {
		var r ModelSourceRow
		if err := rows.Scan(&r.Model, &r.Source, &r.Events,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens,
			&r.Cache1h, &r.Cache5m, &r.MinTS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Ordering keys off the folded name so routing variants of one model sit
	// together and their volumes are added up before models are ranked — the
	// same reason ModelDaily folds before its top-N.
	type slice struct{ model, source string }
	folded := make(map[string]string, len(out))
	byModel := map[string]int64{}
	bySlice := map[slice]int64{}
	for _, r := range out {
		f := model.CanonicalModel(r.Model)
		folded[r.Model] = f
		byModel[f] += r.TotalTokens
		bySlice[slice{f, r.Source}] += r.TotalTokens
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		fa, fb := folded[a.Model], folded[b.Model]
		if ta, tb := byModel[fa], byModel[fb]; ta != tb {
			return ta > tb
		}
		if fa != fb {
			return fa < fb // stable tie-break on equal volume
		}
		// Ranked by the merged slice, not by the individual variant, so the two
		// halves of one routing-split slice cannot be separated by a third.
		if sa, sb := bySlice[slice{fa, a.Source}], bySlice[slice{fb, b.Source}]; sa != sb {
			return sa > sb
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Model < b.Model
	})
	return out, nil
}

// ModelDaily buckets usage by local calendar day and model over [from, to),
// keeping only the topN models by range total; everything else is merged into
// a single ModelOther row per day. Rows are ordered by day ascending, and
// within a day by range volume descending with ModelOther last.
func (s *Store) ModelDaily(from, to time.Time, topN int) ([]ModelDailyRow, error) {
	if topN <= 0 {
		topN = defaultModelTopN
	}
	rows, err := s.rdb.Query(
		`SELECT date(ts/1000, 'unixepoch', 'localtime') AS d, model, `+sums+`
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY d, model`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []ModelDailyRow
	rangeTotal := map[string]int64{}
	for rows.Next() {
		var r ModelDailyRow
		if err := rows.Scan(&r.Bucket, &r.Model, &r.Events,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.TotalTokens); err != nil {
			return nil, err
		}
		// Fold before the top-N is decided, for the same reason Breakdown does
		// (ADR-less, see internal/model/canonical.go): unfolded, one model's
		// two routing variants compete for separate slots, so the chart shows
		// `claude-opus-4-8` and `anthropic.claude-opus-4-8` as rival series and
		// each understates the model it belongs to.
		r.Model = model.CanonicalModel(r.Model)
		rangeTotal[r.Model] += r.TotalTokens
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	keep := topModels(rangeTotal, topN)
	// Merge per (bucket, effective model); map iteration order is random, so
	// the result is sorted explicitly below.
	merged := map[string]map[string]*ModelDailyRow{}
	for _, r := range raw {
		name := r.Model
		if !keep[name] {
			name = ModelOther
		}
		day := merged[r.Bucket]
		if day == nil {
			day = map[string]*ModelDailyRow{}
			merged[r.Bucket] = day
		}
		cur := day[name]
		if cur == nil {
			cur = &ModelDailyRow{Bucket: r.Bucket, Model: name}
			day[name] = cur
		}
		addTotals(&cur.Totals, r.Totals)
	}

	out := make([]ModelDailyRow, 0, len(raw))
	for _, day := range merged {
		for _, r := range day {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Bucket != b.Bucket {
			return a.Bucket < b.Bucket
		}
		if ao, bo := a.Model == ModelOther, b.Model == ModelOther; ao != bo {
			return bo // ModelOther sinks to the end of its day
		}
		if ta, tb := rangeTotal[a.Model], rangeTotal[b.Model]; ta != tb {
			return ta > tb
		}
		return a.Model < b.Model
	})
	return out, nil
}

// topModels picks the n highest-volume models, ties broken by name so the
// selection is deterministic across calls.
func topModels(total map[string]int64, n int) map[string]bool {
	names := make([]string, 0, len(total))
	for m := range total {
		names = append(names, m)
	}
	sort.Slice(names, func(i, j int) bool {
		if total[names[i]] != total[names[j]] {
			return total[names[i]] > total[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	keep := make(map[string]bool, len(names))
	for _, m := range names {
		keep[m] = true
	}
	return keep
}

func addTotals(dst *Totals, src Totals) {
	dst.Events += src.Events
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheRead += src.CacheRead
	dst.CacheCreation += src.CacheCreation
	dst.TotalTokens += src.TotalTokens
}
