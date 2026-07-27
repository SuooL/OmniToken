package server

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// handleReports serves aggregate report rows (F12) as JSON or CSV downloads.
// GET /api/v1/reports?granularity=daily|weekly|monthly|session&days=30&format=json|csv
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	gran := r.URL.Query().Get("granularity")
	if gran == "" {
		gran = "daily"
	}
	days := queryInt(r, "days", 30)
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	from := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	var rows any
	var writeCSV func(*csv.Writer) error
	if gran == "session" {
		srows, err := s.store.SessionRows(from, end, queryInt(r, "limit", 200))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows = srows
		writeCSV = func(cw *csv.Writer) error { return sessionCSV(cw, srows) }
	} else {
		brows, err := s.store.PeriodRows(gran, from, end)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest) // unknown granularity
			return
		}
		rows = brows
		writeCSV = func(cw *csv.Writer) error { return periodCSV(cw, brows) }
	}

	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=omnitoken-"+gran+".csv")
		cw := csv.NewWriter(w)
		if err := writeCSV(cw); err != nil {
			return // headers already sent; just stop
		}
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"granularity": gran, "days": days, "rows": rows})
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func totalsCSV(t store.Totals) []string {
	return []string{i64(t.Events), i64(t.InputTokens), i64(t.OutputTokens),
		i64(t.CacheRead), i64(t.CacheCreation), i64(t.TotalTokens)}
}

var totalsHeader = []string{"events", "input_tokens", "output_tokens",
	"cache_read_tokens", "cache_creation_tokens", "total_tokens"}

func periodCSV(cw *csv.Writer, rows []store.BucketRow) error {
	if err := cw.Write(append([]string{"bucket"}, totalsHeader...)); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write(append([]string{r.Bucket}, totalsCSV(r.Totals)...)); err != nil {
			return err
		}
	}
	return nil
}

func sessionCSV(cw *csv.Writer, rows []store.SessionRow) error {
	head := append([]string{"session_id", "device", "source", "repo", "model", "first_time", "last_time"}, totalsHeader...)
	if err := cw.Write(head); err != nil {
		return err
	}
	const layout = "2006-01-02 15:04:05"
	for _, r := range rows {
		rec := append([]string{r.SessionID, r.Device, r.Source, r.Repo, r.Model,
			time.UnixMilli(r.FirstTS).Format(layout), time.UnixMilli(r.LastTS).Format(layout)},
			totalsCSV(r.Totals)...)
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	return nil
}
