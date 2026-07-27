package store

import "time"

// HeatmapDay is one local calendar day of activity, carrying only the two
// numbers the calendar heatmap encodes (F20/GAP-2): total tokens drive the
// cell colour, request count rides along in the tooltip.
type HeatmapDay struct {
	Bucket string `json:"bucket"` // local calendar day, "YYYY-MM-DD"
	Tokens int64  `json:"tokens"`
	Events int64  `json:"events"`
}

// HeatmapDays buckets events in [from, to) by local calendar day, ascending.
//
// Days without events are simply absent: the grid is a full year of cells but
// the payload stays proportional to days actually worked, and the client has
// to lay out the Mon..Sun columns anyway. Bucketing matches Daily (same
// date(..., 'localtime') expression), so the heatmap and the daily chart can
// never disagree about which day a request belongs to.
func (s *Store) HeatmapDays(from, to time.Time) ([]HeatmapDay, error) {
	rows, err := s.db.Query(
		`SELECT date(ts/1000, 'unixepoch', 'localtime') AS d,
		        COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0),
		        COUNT(*)
		 FROM events WHERE ts >= ? AND ts < ? GROUP BY d ORDER BY d`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeatmapDay
	for rows.Next() {
		var r HeatmapDay
		if err := rows.Scan(&r.Bucket, &r.Tokens, &r.Events); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
