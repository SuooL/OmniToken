package store

import (
	"sort"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

type TelemetryModelUsage struct {
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Share  float64 `json:"share"`
}

type TelemetryTodayUsage struct {
	StartMS     int64                 `json:"start_ms"`
	EndMS       int64                 `json:"end_ms"`
	TotalTokens int64                 `json:"total_tokens"`
	Models      []TelemetryModelUsage `json:"models"`
}

type TelemetrySourceUsage struct {
	Source         string   `json:"source"`
	Tokens         int64    `json:"tokens"`
	PreviousTokens int64    `json:"previous_tokens"`
	ChangePercent  *float64 `json:"change_percent"`
}

type TelemetryRollingUsage struct {
	StartMS     int64                  `json:"start_ms"`
	EndMS       int64                  `json:"end_ms"`
	TotalTokens int64                  `json:"total_tokens"`
	Sources     []TelemetrySourceUsage `json:"sources"`
}

type TelemetryUsageSnapshot struct {
	Today     TelemetryTodayUsage   `json:"today"`
	Rolling5H TelemetryRollingUsage `json:"rolling_5h"`
}

type TelemetryBucket struct {
	StartMS      int64               `json:"start_ms"`
	AggregateTPS float64             `json:"aggregate_tps"`
	ActiveMS     int64               `json:"active_ms"`
	Sources      []SpeedContribution `json:"sources"`
}

type TelemetrySourceCoverage struct {
	Key                  string `json:"key"`
	TotalEvents          int64  `json:"total_events"`
	MeasuredEvents       int64  `json:"measured_events"`
	TotalOutputTokens    int64  `json:"total_output_tokens"`
	MeasuredOutputTokens int64  `json:"measured_output_tokens"`
}

type TelemetrySpeedSeries struct {
	StartMS           int64                     `json:"start_ms"`
	EndMS             int64                     `json:"end_ms"`
	BucketMS          int64                     `json:"bucket_ms"`
	MeasuredSources   []string                  `json:"measured_sources"`
	UnmeasuredSources []string                  `json:"unmeasured_sources"`
	Coverage          []TelemetrySourceCoverage `json:"coverage"`
	Series            []TelemetryBucket         `json:"series"`
}

const telemetryTokenExpression = `input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens`

// TelemetryUsage returns calendar-day usage plus fixed rolling five-hour and
// immediately preceding five-hour source comparisons. Claude and Codex rows
// are intentionally stable for their dedicated cards; any other source is
// folded into an API row so the visible source rows reconcile to TotalTokens.
func (s *Store) TelemetryUsage(todayStart, now time.Time) (TelemetryUsageSnapshot, error) {
	out := TelemetryUsageSnapshot{
		Today: TelemetryTodayUsage{
			StartMS: todayStart.UnixMilli(),
			EndMS:   now.UnixMilli(),
			Models:  []TelemetryModelUsage{},
		},
		Rolling5H: TelemetryRollingUsage{
			StartMS: now.Add(-5 * time.Hour).UnixMilli(),
			EndMS:   now.UnixMilli(),
			Sources: []TelemetrySourceUsage{},
		},
	}

	rows, err := s.db.Query(
		`SELECT model, COALESCE(SUM(`+telemetryTokenExpression+`),0)
		 FROM events WHERE ts >= ? AND ts <= ?
		 GROUP BY model`,
		out.Today.StartMS, out.Today.EndMS)
	if err != nil {
		return out, err
	}
	byModel := map[string]int64{}
	for rows.Next() {
		var key string
		var tokens int64
		if err := rows.Scan(&key, &tokens); err != nil {
			rows.Close()
			return out, err
		}
		key = model.CanonicalModel(key)
		if key == "" {
			key = "unknown"
		}
		byModel[key] += tokens
		out.Today.TotalTokens += tokens
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	for key, tokens := range byModel {
		if tokens <= 0 {
			continue
		}
		row := TelemetryModelUsage{Model: key, Tokens: tokens}
		if out.Today.TotalTokens > 0 {
			row.Share = float64(tokens) / float64(out.Today.TotalTokens)
		}
		out.Today.Models = append(out.Today.Models, row)
	}
	sort.Slice(out.Today.Models, func(i, j int) bool {
		if out.Today.Models[i].Tokens == out.Today.Models[j].Tokens {
			return out.Today.Models[i].Model < out.Today.Models[j].Model
		}
		return out.Today.Models[i].Tokens > out.Today.Models[j].Tokens
	})

	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(`+telemetryTokenExpression+`),0)
		 FROM events WHERE ts >= ? AND ts <= ?`,
		out.Rolling5H.StartMS, out.Rolling5H.EndMS,
	).Scan(&out.Rolling5H.TotalTokens); err != nil {
		return out, err
	}

	previousStartMS := now.Add(-10 * time.Hour).UnixMilli()
	sourceRows, err := s.db.Query(
		`SELECT source,
		        COALESCE(SUM(CASE WHEN ts >= ? THEN `+telemetryTokenExpression+` ELSE 0 END),0),
		        COALESCE(SUM(CASE WHEN ts >= ? AND ts < ? THEN `+telemetryTokenExpression+` ELSE 0 END),0)
		 FROM events
		 WHERE ts >= ? AND ts <= ?
		 GROUP BY source`,
		out.Rolling5H.StartMS,
		previousStartMS, out.Rolling5H.StartMS,
		previousStartMS, out.Rolling5H.EndMS)
	if err != nil {
		return out, err
	}
	bySource := map[string][2]int64{}
	for sourceRows.Next() {
		var source string
		var current, previous int64
		if err := sourceRows.Scan(&source, &current, &previous); err != nil {
			sourceRows.Close()
			return out, err
		}
		key := speedSourceKey(source)
		totals := bySource[key]
		totals[0] += current
		totals[1] += previous
		bySource[key] = totals
	}
	if err := sourceRows.Err(); err != nil {
		sourceRows.Close()
		return out, err
	}
	sourceRows.Close()
	sourceOrder := []string{"claude-code", "codex"}
	var extraSources []string
	for source := range bySource {
		if source != "claude-code" && source != "codex" {
			extraSources = append(extraSources, source)
		}
	}
	sort.Strings(extraSources)
	sourceOrder = append(sourceOrder, extraSources...)
	for _, source := range sourceOrder {
		totals := bySource[source]
		row := TelemetrySourceUsage{
			Source:         source,
			Tokens:         totals[0],
			PreviousTokens: totals[1],
		}
		if totals[1] > 0 {
			change := float64(totals[0]-totals[1]) * 100 / float64(totals[1])
			row.ChangePercent = &change
		}
		out.Rolling5H.Sources = append(out.Rolling5H.Sources, row)
	}
	return out, nil
}

// TelemetrySpeedSeries returns a bounded sequence of source contributions.
// Every source row in a bucket shares the bucket's global ActiveMS denominator.
func (s *Store) TelemetrySpeedSeries(from, to time.Time, bucket time.Duration) (TelemetrySpeedSeries, error) {
	bucketMS := bucket.Milliseconds()
	if bucketMS <= 0 {
		bucketMS = time.Minute.Milliseconds()
	}
	startMS, endMS := from.UnixMilli(), to.UnixMilli()
	out := TelemetrySpeedSeries{
		StartMS:           startMS,
		EndMS:             endMS,
		BucketMS:          bucketMS,
		MeasuredSources:   []string{},
		UnmeasuredSources: []string{},
		Coverage:          []TelemetrySourceCoverage{},
		Series:            []TelemetryBucket{},
	}
	if endMS <= startMS {
		return out, nil
	}
	n := int((endMS - startMS + bucketMS - 1) / bucketMS)
	out.Series = make([]TelemetryBucket, n)
	grouped := make([]map[string]*speedContributionAcc, n)
	globalSpans := make([][]span, n)
	for i := range out.Series {
		out.Series[i].StartMS = startMS + int64(i)*bucketMS
		out.Series[i].Sources = []SpeedContribution{}
		grouped[i] = map[string]*speedContributionAcc{}
	}

	rows, err := s.db.Query(
		`SELECT source, ts, gen_ms, output_tokens
		 FROM events
		 WHERE ts > ? AND ts <= ? AND gen_ms > 0 AND output_tokens > 0`,
		startMS, endMS)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var source string
		var ts, genMS, outputTokens int64
		if err := rows.Scan(&source, &ts, &genMS, &outputTokens); err != nil {
			rows.Close()
			return out, err
		}
		eventStart, eventEnd := ts-genMS, ts
		eventDuration := eventEnd - eventStart
		if eventDuration <= 0 {
			continue
		}
		first := int((max(eventStart, startMS) - startMS) / bucketMS)
		last := int((min(eventEnd, endMS) - startMS) / bucketMS)
		key := speedSourceKey(source)
		type bucketAllocation struct {
			index     int
			interval  span
			tokens    int64
			remainder int64
		}
		var allocations []bucketAllocation
		var coveredDuration, allocatedTokens int64
		for i := max(first, 0); i <= min(last, n-1); i++ {
			bucketStart := out.Series[i].StartMS
			bucketEnd := min(bucketStart+bucketMS, endMS)
			lo, hi := max(eventStart, bucketStart), min(eventEnd, bucketEnd)
			if hi <= lo {
				continue
			}
			overlap := hi - lo
			numerator := outputTokens * overlap
			allocation := bucketAllocation{
				index:     i,
				interval:  span{lo, hi},
				tokens:    numerator / eventDuration,
				remainder: numerator % eventDuration,
			}
			allocations = append(allocations, allocation)
			coveredDuration += overlap
			allocatedTokens += allocation.tokens
		}
		targetTokens := outputTokens * coveredDuration / eventDuration
		sort.SliceStable(allocations, func(i, j int) bool {
			if allocations[i].remainder == allocations[j].remainder {
				return allocations[i].index < allocations[j].index
			}
			return allocations[i].remainder > allocations[j].remainder
		})
		for i := int64(0); i < targetTokens-allocatedTokens; i++ {
			allocations[i].tokens++
		}
		for _, allocation := range allocations {
			addSpeedContribution(
				grouped[allocation.index], key, allocation.tokens, allocation.interval,
			)
			globalSpans[allocation.index] = append(
				globalSpans[allocation.index], allocation.interval,
			)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	for i := range out.Series {
		for _, merged := range mergeSpans(globalSpans[i]) {
			out.Series[i].ActiveMS += merged.end - merged.start
		}
		out.Series[i].Sources = buildSpeedContributions(grouped[i], out.Series[i].ActiveMS)
		sort.Slice(out.Series[i].Sources, func(a, b int) bool {
			return out.Series[i].Sources[a].Key < out.Series[i].Sources[b].Key
		})
		var outputTokens int64
		for _, source := range out.Series[i].Sources {
			outputTokens += source.OutputTokens
		}
		if out.Series[i].ActiveMS > 0 {
			out.Series[i].AggregateTPS = float64(outputTokens) * 1000 / float64(out.Series[i].ActiveMS)
		}
	}

	// Coverage is per source and honest by construction: the denominator is
	// every event, the numerator only those carrying an interval. Codex sits at
	// roughly 90% of events (turns Codex did not time, and turns replayed into
	// a later rollout, have none), and the panel must show that rather than
	// treating the rest as zero speed.
	coverageRows, err := s.db.Query(
		`SELECT source,
		        COUNT(*),
		        COALESCE(SUM(CASE WHEN gen_ms > 0 THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(CASE WHEN gen_ms > 0 THEN output_tokens ELSE 0 END),0)
		 FROM events
		 WHERE ts > ? AND ts <= ? AND output_tokens > 0
		 GROUP BY source`,
		startMS, endMS)
	if err != nil {
		return out, err
	}
	coverageByKey := map[string]*TelemetrySourceCoverage{}
	for coverageRows.Next() {
		var source string
		var row TelemetrySourceCoverage
		if err := coverageRows.Scan(
			&source, &row.TotalEvents, &row.MeasuredEvents,
			&row.TotalOutputTokens, &row.MeasuredOutputTokens,
		); err != nil {
			coverageRows.Close()
			return out, err
		}
		row.Key = speedSourceKey(source)
		acc := coverageByKey[row.Key]
		if acc == nil {
			cp := row
			coverageByKey[row.Key] = &cp
			continue
		}
		acc.TotalEvents += row.TotalEvents
		acc.MeasuredEvents += row.MeasuredEvents
		acc.TotalOutputTokens += row.TotalOutputTokens
		acc.MeasuredOutputTokens += row.MeasuredOutputTokens
	}
	if err := coverageRows.Err(); err != nil {
		coverageRows.Close()
		return out, err
	}
	coverageRows.Close()
	for _, row := range coverageByKey {
		out.Coverage = append(out.Coverage, *row)
	}
	sort.Slice(out.Coverage, func(i, j int) bool { return out.Coverage[i].Key < out.Coverage[j].Key })
	for _, row := range out.Coverage {
		if row.MeasuredEvents > 0 {
			out.MeasuredSources = append(out.MeasuredSources, row.Key)
		}
		if row.MeasuredEvents == 0 {
			out.UnmeasuredSources = append(out.UnmeasuredSources, row.Key)
		}
	}
	sort.Strings(out.MeasuredSources)
	sort.Strings(out.UnmeasuredSources)
	return out, nil
}
