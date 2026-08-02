package store

import (
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Speed over time and per model, on the union basis ADR-0009 settled on:
// Σoutput_tokens ÷ |union of generation intervals|, never a mean of per-event
// ratios. An event's interval is [ts-gen_ms, ts]; gen_ms = 0 means the interval
// is unknown (Codex, or history collected before the field existed), and such
// events are excluded rather than treated as instantaneous.

// SpeedBucket is one point on the speed curve.
//
// ActiveMS is what separates "nothing was generating" from "generating slowly":
// a bucket with no generation has ActiveMS 0, and the panel must draw a gap
// there rather than a zero, which would claim the model emitted nothing while
// running.
type SpeedBucket struct {
	StartMS      int64   `json:"start_ms"`
	OutputTokens int64   `json:"output_tokens"`
	ActiveMS     int64   `json:"active_ms"`
	TPS          float64 `json:"tps"`
}

// SpeedSeries buckets generation speed across [from, to).
//
// Every bucket in the range is returned, including idle ones, so the caller can
// plot a continuous time axis without reconstructing the gaps.
//
// An interval that straddles a bucket boundary contributes to both, and its
// tokens are split by how much of the interval falls in each — the alternative,
// charging the whole response to the bucket it finished in, makes a long
// generation look like a spike at its end.
func (s *Store) SpeedSeries(from, to time.Time, bucket time.Duration, device string) ([]SpeedBucket, error) {
	bucketMS := bucket.Milliseconds()
	if bucketMS <= 0 {
		bucketMS = time.Minute.Milliseconds()
	}
	startMS, endMS := from.UnixMilli(), to.UnixMilli()
	n := int((endMS - startMS) / bucketMS)
	if n <= 0 {
		return []SpeedBucket{}, nil
	}

	out := make([]SpeedBucket, n)
	spansPer := make([][]span, n)
	for i := range out {
		out[i].StartMS = startMS + int64(i)*bucketMS
	}

	// ts is the interval's END, so an event that finished inside the window can
	// have started before it; the lower bound stays open and clipping below
	// keeps the out-of-window part from counting.
	q := `SELECT ts, gen_ms, output_tokens FROM events
	      WHERE ts > ? AND ts <= ? AND gen_ms > 0 AND output_tokens > 0`
	args := []any{startMS, endMS}
	if device != "" {
		q += ` AND device = ?`
		args = append(args, device)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ts, genMS, outTok int64
		if err := rows.Scan(&ts, &genMS, &outTok); err != nil {
			return nil, err
		}
		evStart, evEnd := ts-genMS, ts
		total := evEnd - evStart
		if total <= 0 {
			continue
		}
		// Only the buckets the interval actually touches. Walking all of them
		// per event is fine for an hour at one-minute resolution and quadratic
		// for a month of it.
		first := int((max(evStart, startMS) - startMS) / bucketMS)
		last := int((min(evEnd, endMS) - startMS) / bucketMS)
		for i := max(first, 0); i <= min(last, n-1); i++ {
			bStart := out[i].StartMS
			bEnd := bStart + bucketMS
			lo, hi := max(evStart, bStart), min(evEnd, bEnd)
			if hi <= lo {
				continue
			}
			// Tokens follow time: the share of the interval inside this bucket.
			out[i].OutputTokens += outTok * (hi - lo) / total
			spansPer[i] = append(spansPer[i], span{lo, hi})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		for _, sp := range mergeSpans(spansPer[i]) {
			out[i].ActiveMS += sp.end - sp.start
		}
		if out[i].ActiveMS > 0 {
			out[i].TPS = float64(out[i].OutputTokens) * 1000 / float64(out[i].ActiveMS)
		}
	}
	return out, nil
}

// SpeedModelStat is one model's generation speed over a range.
type SpeedModelStat struct {
	Model        string  `json:"model"`
	OutputTokens int64   `json:"output_tokens"`
	ActiveMS     int64   `json:"active_ms"`
	TPS          float64 `json:"tps"`
	Samples      int64   `json:"samples"`  // events carrying a generation interval
	Streams      int64   `json:"streams"`  // distinct (device, session) streams
	Coverage     float64 `json:"coverage"` // samples ÷ all events of this model
	// TTFT is measured, never derived: the proxy times it around the request,
	// Codex writes it into task_complete. It has its own sample count because
	// it exists for a different subset than the interval does — a Codex turn
	// reports one TTFT however many requests it made.
	TTFTSamples  int64   `json:"ttft_samples"`
	MedianTTFTMS float64 `json:"median_ttft_ms"`
	P90TTFTMS    float64 `json:"p90_ttft_ms"`
	// Sources says which channels measured this row. It is not decoration: a
	// Codex row's interval contains the turn's tool calls, so its speed is a
	// lower bound, and the page cannot label that honestly without knowing
	// which rows came from where.
	Sources []string `json:"sources"`
}

// No per-response distribution is reported here, and that is a finding rather
// than an omission. A log-derived interval is [previous user record → this
// reply], which contains the wait for the first token as well as the
// generation. For a long answer that hardly matters; for the short ones the
// wait IS the interval, and the ratio stops describing speed — measured on this
// machine, 69% of claude-sonnet-5's responses are under 8 tokens, dragging its
// per-response median to 0.7 tok/s against 57.6 aggregate. Separating latency
// from generation needs the two measured apart, which is exactly what the proxy
// channel does (speed.go), so medians live there and only there.

// SpeedByModelUnion compares models on the union basis.
//
// The denominator is unioned **per stream** — one (device, session_id) — and
// then summed across streams. Both halves of that matter:
//
//   - unioning within a stream stops a session's overlapping subagents from
//     charging the same second twice (ADR-0009: subagent output is filed under
//     the parent's session_id);
//   - NOT unioning across streams keeps this a per-stream speed. Merging two
//     machines that generated simultaneously would divide both machines' tokens
//     by one machine's wall clock and report a model twice as fast as any
//     stream of it ever ran.
//
// Coverage is reported because it is routinely below 100%: Claude Code deletes
// logs after 30 days, so events older than a rescan can never be given an
// interval. A speed built on 12% of the events is not a fact about the model.
func (s *Store) SpeedByModelUnion(from, to time.Time) ([]SpeedModelStat, error) {
	rows, err := s.db.Query(
		`SELECT model, device, session_id, ts, gen_ms, output_tokens, ttft_ms, source
		 FROM events
		 WHERE ts >= ? AND ts < ? AND gen_ms > 0 AND output_tokens > 0`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type streamKey struct{ model, device, session string }
	streams := map[streamKey][]span{}
	tokens := map[string]int64{}
	samples := map[string]int64{}
	ttfts := map[string][]int64{}
	sources := map[string]map[string]bool{}
	for rows.Next() {
		var mdl, device, session, source string
		var ts, genMS, outTok, ttftMS int64
		if err := rows.Scan(&mdl, &device, &session, &ts, &genMS, &outTok, &ttftMS, &source); err != nil {
			return nil, err
		}
		// Same folding as every other view: two ids for one model must not
		// split its speed in half (internal/model/canonical.go).
		mdl = model.CanonicalModel(mdl)
		k := streamKey{mdl, device, session}
		streams[k] = append(streams[k], span{ts - genMS, ts})
		tokens[mdl] += outTok
		samples[mdl]++
		if ttftMS > 0 {
			ttfts[mdl] = append(ttfts[mdl], ttftMS)
		}
		if source != "" {
			if sources[mdl] == nil {
				sources[mdl] = map[string]bool{}
			}
			sources[mdl][source] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	active := map[string]int64{}
	streamCount := map[string]int64{}
	for k, sp := range streams {
		streamCount[k.model]++
		for _, m := range mergeSpans(sp) {
			active[k.model] += m.end - m.start
		}
	}

	totals, err := s.eventCountByModel(from, to)
	if err != nil {
		return nil, err
	}

	out := make([]SpeedModelStat, 0, len(tokens))
	for mdl, tok := range tokens {
		st := SpeedModelStat{
			Model: mdl, OutputTokens: tok, ActiveMS: active[mdl],
			Samples: samples[mdl], Streams: streamCount[mdl],
		}
		if st.ActiveMS > 0 {
			st.TPS = float64(tok) * 1000 / float64(st.ActiveMS)
		}
		if all := totals[mdl]; all > 0 {
			st.Coverage = float64(st.Samples) / float64(all)
		}
		st.TTFTSamples = int64(len(ttfts[mdl]))
		st.MedianTTFTMS, st.P90TTFTMS = ttftQuantiles(ttfts[mdl])
		st.Sources = make([]string, 0, len(sources[mdl]))
		for source := range sources[mdl] {
			st.Sources = append(st.Sources, source)
		}
		sort.Strings(st.Sources)
		out = append(out, st)
	}
	sortModelStats(out)
	return out, nil
}

// ttftQuantiles returns the median and the nearest-rank P90 of one model's
// first-token latencies, matching how the proxy channel ranks its own
// (speed.go). Unlike speed, these are not merged with anything: a latency is a
// latency whether it was measured around a request or reported by a turn.
func ttftQuantiles(values []int64) (median, p90 float64) {
	n := len(values)
	if n == 0 {
		return 0, 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if n%2 == 1 {
		median = float64(sorted[n/2])
	} else {
		median = float64(sorted[n/2-1]+sorted[n/2]) / 2
	}
	rank := (9*n + 9) / 10
	return median, float64(sorted[min(rank, n)-1])
}

// eventCountByModel counts every event per model, interval or not, so coverage
// has an honest denominator.
func (s *Store) eventCountByModel(from, to time.Time) (map[string]int64, error) {
	rows, err := s.db.Query(
		`SELECT model, COUNT(*) FROM events
		 WHERE ts >= ? AND ts < ? AND output_tokens > 0
		 GROUP BY model`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var mdl string
		var n int64
		if err := rows.Scan(&mdl, &n); err != nil {
			return nil, err
		}
		out[model.CanonicalModel(mdl)] += n
	}
	return out, rows.Err()
}

func sortModelStats(xs []SpeedModelStat) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j].OutputTokens > xs[j-1].OutputTokens; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
