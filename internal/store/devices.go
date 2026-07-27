package store

import "time"

// Per-device queries backing the standalone devices page (F21 / GAP-3).
// The overview already answers "how much per device"; this file answers
// "how did each device behave over time, and what does it work on".

// DeviceDailyRow is one (device, local day) cell of the stacked trend.
// Days with no events for a device simply have no row — the caller fills
// the gaps, so the payload stays sparse for idle devices.
type DeviceDailyRow struct {
	Bucket string `json:"bucket"` // e.g. "2026-07-25"
	Device string `json:"device"`
	Tokens int64  `json:"total_tokens"`
	Events int64  `json:"events"`
}

// DeviceSummaryRow carries per-device totals plus the context the page shows
// next to them: when the device last reported, how many distinct repos it
// touched, and its dominant model. Models keeps the per-model breakdown with
// the cache-TTL split so the caller can price the device at query time
// (ADR-0005) without a second round trip; it is sorted by tokens descending,
// so Models[0] is the dominant model.
type DeviceSummaryRow struct {
	Device string `json:"device"`
	Totals
	LastTS         int64           `json:"last_ts"`
	Repos          int64           `json:"repos"`
	TopModel       string          `json:"top_model"`
	TopModelTokens int64           `json:"top_model_tokens"`
	Models         []ModelUsageRow `json:"models"`
}

// DeviceDaily aggregates tokens/events per (device, local calendar day),
// ordered by day then device so the caller can stack deterministically.
func (s *Store) DeviceDaily(from, to time.Time) ([]DeviceDailyRow, error) {
	rows, err := s.db.Query(
		`SELECT date(ts/1000, 'unixepoch', 'localtime') AS d, device,
		        COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0),
		        COUNT(*)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY d, device ORDER BY d, device`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceDailyRow
	for rows.Next() {
		var r DeviceDailyRow
		if err := rows.Scan(&r.Bucket, &r.Device, &r.Tokens, &r.Events); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeviceSummary returns one row per device with events in [from, to),
// busiest device first. Repos counts distinct non-empty repos only —
// events outside a git checkout carry repo='' and must not inflate the count.
func (s *Store) DeviceSummary(from, to time.Time) ([]DeviceSummaryRow, error) {
	models, err := s.deviceModelUsage(from, to)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT device, `+sums+`,
		        COALESCE(MAX(ts),0),
		        COUNT(DISTINCT CASE WHEN repo != '' THEN repo END)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY device
		 ORDER BY SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens) DESC, device`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceSummaryRow
	for rows.Next() {
		var r DeviceSummaryRow
		if err := rows.Scan(&r.Device, &r.Events, &r.InputTokens, &r.OutputTokens,
			&r.CacheRead, &r.CacheCreation, &r.TotalTokens, &r.LastTS, &r.Repos); err != nil {
			return nil, err
		}
		r.Models = models[r.Device]
		if len(r.Models) > 0 {
			r.TopModel = r.Models[0].Model
			r.TopModelTokens = r.Models[0].TotalTokens
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// deviceModelUsage groups per-model usage under each device, biggest model
// first. Grouping is by model alone (provider is reported as a sample, not a
// grouping key) so a model served through two providers stays one row for
// the "dominant model" question.
func (s *Store) deviceModelUsage(from, to time.Time) (map[string][]ModelUsageRow, error) {
	rows, err := s.db.Query(
		`SELECT device, model, MAX(provider), `+sums+`,
		        COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0), COALESCE(MIN(ts),0)
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY device, model
		 ORDER BY device, SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens) DESC`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ModelUsageRow{}
	for rows.Next() {
		var device string
		var m ModelUsageRow
		if err := rows.Scan(&device, &m.Model, &m.Provider, &m.Events, &m.InputTokens, &m.OutputTokens,
			&m.CacheRead, &m.CacheCreation, &m.TotalTokens, &m.Cache1h, &m.Cache5m, &m.MinTS); err != nil {
			return nil, err
		}
		out[device] = append(out[device], m)
	}
	return out, rows.Err()
}
