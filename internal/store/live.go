package store

import (
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Live-view queries (F10).

type DeviceStatus struct {
	Device      string `json:"device"`
	LastTS      int64  `json:"last_ts"`
	TodayTokens int64  `json:"today_tokens"`
	TodayEvents int64  `json:"today_events"`
}

func (s *Store) DeviceStatuses(todayStart time.Time) ([]DeviceStatus, error) {
	rows, err := s.db.Query(
		`SELECT device, MAX(ts),
		        COALESCE(SUM(CASE WHEN ts >= ?1 THEN input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens ELSE 0 END),0),
		        COALESCE(SUM(CASE WHEN ts >= ?1 THEN 1 ELSE 0 END),0)
		 FROM events GROUP BY device ORDER BY MAX(ts) DESC`,
		todayStart.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceStatus
	for rows.Next() {
		var d DeviceStatus
		if err := rows.Scan(&d.Device, &d.LastTS, &d.TodayTokens, &d.TodayEvents); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type LiveSession struct {
	SessionID string `json:"session_id"`
	Device    string `json:"device"`
	Source    string `json:"source"`
	Repo      string `json:"repo"`
	CWD       string `json:"cwd"`
	Model     string `json:"model"`
	LastTS    int64  `json:"last_ts"`
	Tokens    int64  `json:"tokens"`
	Events    int64  `json:"events"`
}

// ActiveSessions lists sessions with any event since the cutoff, newest first.
func (s *Store) ActiveSessions(since time.Time) ([]LiveSession, error) {
	rows, err := s.db.Query(
		`SELECT session_id, device, source, MAX(repo), MAX(cwd), MAX(model), MAX(ts),
		        COALESCE(SUM(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0), COUNT(*)
		 FROM events WHERE ts >= ?
		 GROUP BY session_id, device, source ORDER BY MAX(ts) DESC LIMIT 50`,
		since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveSession
	for rows.Next() {
		var ls LiveSession
		if err := rows.Scan(&ls.SessionID, &ls.Device, &ls.Source, &ls.Repo, &ls.CWD, &ls.Model, &ls.LastTS, &ls.Tokens, &ls.Events); err != nil {
			return nil, err
		}
		// The grouping key is the session, so this fold only renames what is
		// shown — it cannot merge two rows the way it does in a GROUP BY view.
		ls.Model = model.CanonicalModel(ls.Model)
		out = append(out, ls)
	}
	return out, rows.Err()
}

// TokensSince returns total tokens and output tokens since the cutoff.
// TokensSince returns the burn-rate numerator and its output component.
//
// cache_read is deliberately excluded. It is the same conversation context
// re-read on every turn, so counting it makes the rate describe repetition
// rather than consumption: measured on real traffic it was 99.6% of the total
// (9.28M of 9.31M over ten minutes, across 14 requests), which turned a ~3K/min
// figure into ~862K/min. It is also billed at a tenth of the input price and
// does not consume the 5h/weekly windows at anything like that rate.
//
// Same call as abtop's token_rate, which counts input + output + cache_create
// and excludes cache_read for the same reason. Cache volume is not lost — the
// cache page reports it, where repetition is the point.
func (s *Store) TokensSince(since time.Time) (total, output int64, err error) {
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens),0),
		        COALESCE(SUM(output_tokens),0)
		 FROM events WHERE ts >= ?`, since.UnixMilli()).Scan(&total, &output)
	return
}
