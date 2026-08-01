package store

import (
	"sort"
	"time"
)

// Capacity calibration (ADR-0025): how many tokens one subscription window
// actually holds.
//
// The provider reports a percentage and never the allowance behind it, so the
// allowance has to be learned. The obvious `tokens ÷ percent` on the window in
// front of you does not work — measured on one real 5-hour window, the tokens
// behind one percentage point ranged from 2.3M to 10.9M, and the implied
// capacity drifted from 880M early to 470M late. The provider's meter is not a
// linear function of anything observable from here.
//
// What does work is treating each window as a single noisy measurement and
// accumulating them. Two things make a measurement worth keeping:
//
//   - it is taken at the window's peak percentage, because an integer
//     percentage carries a relative error of 0.5/pct — ±50% at 1%, ±1.7% at 30%;
//   - the window got far enough to matter. Raising that bar from 10% to 30%
//     halved the spread across 23 real windows (40.7% → 26.0%).
//
// Samples live in their own table rather than being recomputed on demand, and
// the reason is not speed: Claude Code keeps 30 days of logs, and a sample has
// to outlive the events it was derived from.

const capacitySchema = `
CREATE TABLE IF NOT EXISTS quota_capacity (
	source TEXT NOT NULL,
	scope TEXT NOT NULL,
	window_minutes INTEGER NOT NULL,
	window_id INTEGER NOT NULL,
	peak_percent REAL NOT NULL,
	tokens_at_peak INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (source, scope, window_minutes, window_id)
);
`

// windowIDMinutes reduces a reset instant to the window's identity.
//
// Minute resolution for the same reason LatestQuotas uses it: Codex derives the
// boundary from "resets in N seconds", so consecutive observations of one
// window land milliseconds apart. Keyed raw, a single window would deposit a
// sample per observation and drown every other window in the estimate.
func windowIDMinutes(resetsAt int64) int64 { return resetsAt / 60000 }

// CapacityObservation is one look at a live window: how full the provider says
// it is, and how many subscription tokens this fleet has put into it so far.
type CapacityObservation struct {
	Source        string
	Scope         string
	WindowMinutes int
	ResetsAt      int64
	UsedPercent   float64
	Tokens        int64
}

// CapacitySample is one window's contribution to the estimate.
type CapacitySample struct {
	PeakPercent  float64
	TokensAtPeak int64
	WindowID     int64
}

// Capacity returns the tokens this sample implies the whole window holds.
func (s CapacitySample) Capacity() int64 {
	if s.PeakPercent <= 0 {
		return 0
	}
	return int64(float64(s.TokensAtPeak) / s.PeakPercent * 100)
}

// ObserveCapacity records a live reading against its window, keeping the
// highest percentage that window has reached.
//
// Later readings at the same percentage are ignored rather than merged: tokens
// keep climbing while the percentage sits still, and taking the last one would
// pair a bigger numerator with an unchanged denominator — inflating the
// estimate a little more every poll.
//
// Unusable readings are dropped silently. This runs on every collection cycle,
// and a window with no boundary, no percentage or no tokens is the normal state
// of affairs early on, not an error worth surfacing.
func (s *Store) ObserveCapacity(o CapacityObservation) error {
	if o.ResetsAt <= 0 || o.UsedPercent <= 0 || o.Tokens <= 0 || o.WindowMinutes <= 0 {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO quota_capacity
		   (source, scope, window_minutes, window_id, peak_percent, tokens_at_peak, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, scope, window_minutes, window_id) DO UPDATE SET
		   peak_percent = excluded.peak_percent,
		   tokens_at_peak = excluded.tokens_at_peak,
		   updated_at = excluded.updated_at
		 WHERE excluded.peak_percent > quota_capacity.peak_percent`,
		o.Source, o.Scope, o.WindowMinutes, windowIDMinutes(o.ResetsAt),
		o.UsedPercent, o.Tokens, time.Now().UnixMilli())
	return err
}

// capacityMinPeak is the percentage a window must reach to be worth learning
// from. Measured, not chosen: at 10% the spread across 23 real windows was
// 40.7%, at 30% it was 26.0%.
const capacityMinPeak = 30.0

// capacityWindowMemory is how many recent windows the estimate considers.
// Bounded so a plan change or a shift in the provider's accounting ages out on
// its own — nobody has to clear a cache.
const capacityWindowMemory = 30

// capacityMinSamples is the point below which no estimate is offered. Showing
// nothing is better than showing a number derived from two windows.
const capacityMinSamples = 3

// CapacitySamples returns the most recent windows that reached minPeak, newest
// first.
func (s *Store) CapacitySamples(source, scope string, windowMinutes int, minPeak float64, limit int) ([]CapacitySample, error) {
	rows, err := s.db.Query(
		`SELECT peak_percent, tokens_at_peak, window_id FROM quota_capacity
		 WHERE source = ? AND scope = ? AND window_minutes = ? AND peak_percent >= ?
		 ORDER BY window_id DESC LIMIT ?`,
		source, scope, windowMinutes, minPeak, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapacitySample
	for rows.Next() {
		var c CapacitySample
		if err := rows.Scan(&c.PeakPercent, &c.TokensAtPeak, &c.WindowID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CapacityEstimate is how many tokens a window of this kind holds, or ok=false
// when too few windows have been observed to say.
//
// The 25th percentile rather than the mean or the median: this number exists so
// the user does not overrun a quota, and both of those sit too high. On the
// same 13 real samples the median was 333M and this is 314M.
func (s *Store) CapacityEstimate(source, scope string, windowMinutes int) (int64, bool, error) {
	samples, err := s.CapacitySamples(source, scope, windowMinutes, capacityMinPeak, capacityWindowMemory)
	if err != nil {
		return 0, false, err
	}
	if len(samples) < capacityMinSamples {
		return 0, false, nil
	}
	caps := make([]int64, 0, len(samples))
	for _, c := range samples {
		if v := c.Capacity(); v > 0 {
			caps = append(caps, v)
		}
	}
	if len(caps) < capacityMinSamples {
		return 0, false, nil
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
	return caps[len(caps)/4], true, nil
}

// BackfillCapacity derives samples from history that predates the calibration
// table, so the estimate does not start from nothing.
//
// Without it the feature is unusable for a long time by construction: five-hour
// windows would need a day to gather three samples and weekly windows three
// weeks. The inputs are already on disk — every quota snapshot ever stored, and
// the events they can be paired with.
//
// Idempotent, because ObserveCapacity only ever raises a window's peak. Running
// it again after more history arrives simply sharpens what is there.
func (s *Store) BackfillCapacity(since time.Time) (int, error) {
	rows, err := s.db.Query(
		`SELECT source, scope, window_minutes, resets_at, used_percent, observed_at
		 FROM (SELECT source, scope, window_minutes, resets_at, used_percent, observed_at,
		              ROW_NUMBER() OVER (
		                PARTITION BY source, scope, window_minutes, resets_at / 60000
		                ORDER BY used_percent DESC, observed_at ASC) AS rn
		       FROM quota_snapshots
		       WHERE observed_at >= ? AND resets_at > 0 AND used_percent >= ?)
		 WHERE rn = 1`,
		since.UnixMilli(), capacityMinPeak)
	if err != nil {
		return 0, err
	}
	type peak struct {
		source, scope string
		windowMinutes int
		resetsAt      int64
		pct           float64
		observedAt    int64
	}
	var peaks []peak
	for rows.Next() {
		var p peak
		if err := rows.Scan(&p.source, &p.scope, &p.windowMinutes, &p.resetsAt, &p.pct, &p.observedAt); err != nil {
			rows.Close()
			return 0, err
		}
		peaks = append(peaks, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	filled := 0
	for _, p := range peaks {
		start := p.resetsAt - int64(p.windowMinutes)*60_000
		tokens, err := s.subscriptionTokens(p.source, start, p.observedAt)
		if err != nil {
			return filled, err
		}
		if tokens == 0 {
			continue // the window's events have aged out of the log
		}
		if err := s.ObserveCapacity(CapacityObservation{
			Source: p.source, Scope: p.scope, WindowMinutes: p.windowMinutes,
			ResetsAt: p.resetsAt, UsedPercent: p.pct, Tokens: tokens,
		}); err != nil {
			return filled, err
		}
		filled++
	}
	return filled, nil
}

// subscriptionTokens sums the traffic that counts against a quota window: only
// the subscription channel does (ADR-0018 §7). The channel is derived in Go
// from the stored provider, exactly as every read path does it, rather than
// being spelled out as a provider list in SQL that would drift from the mapping.
func (s *Store) subscriptionTokens(source string, from, to int64) (int64, error) {
	rows, err := s.db.Query(
		`SELECT provider, COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0)
		 FROM events WHERE source = ? AND ts >= ? AND ts <= ? GROUP BY provider`,
		source, from, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var provider string
		var tokens int64
		if err := rows.Scan(&provider, &tokens); err != nil {
			return 0, err
		}
		if IsSubscription(source, provider) {
			total += tokens
		}
	}
	return total, rows.Err()
}
