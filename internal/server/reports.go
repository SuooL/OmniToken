package server

import (
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// periodReportRow is one report row plus what it cost. CostUSD is a pointer so
// a row whose models have no price omits the field rather than claiming $0
// (ADR-0005) — the ids it was dropped for are named in the response's
// `unpriced`.
type periodReportRow struct {
	store.BucketRow
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

type sessionReportRow struct {
	store.SessionRow
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

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
	var unpriced []string
	if gran == "session" {
		limit := queryInt(r, "limit", 200)
		srows, err := s.store.SessionRows(from, end, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		split, err := s.store.SessionModelUsage(from, end, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		costs, missing := s.costByKey(len(split), func(i int) (string, store.ModelUsageRow) {
			return split[i].SessionID + "\x00" + split[i].Device, split[i].ModelUsageRow
		})
		unpriced = missing
		out := make([]sessionReportRow, 0, len(srows))
		for _, sr := range srows {
			out = append(out, sessionReportRow{SessionRow: sr, CostUSD: costs[sr.SessionID+"\x00"+sr.Device]})
		}
		rows = out
		writeCSV = func(cw *csv.Writer) error { return sessionCSV(cw, out) }
	} else {
		brows, err := s.store.PeriodRows(gran, from, end)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest) // unknown granularity
			return
		}
		split, err := s.store.PeriodModelUsage(gran, from, end)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		costs, missing := s.costByKey(len(split), func(i int) (string, store.ModelUsageRow) {
			return split[i].Bucket, split[i].ModelUsageRow
		})
		unpriced = missing
		out := make([]periodReportRow, 0, len(brows))
		for _, br := range brows {
			out = append(out, periodReportRow{BucketRow: br, CostUSD: costs[br.Bucket]})
		}
		rows = out
		writeCSV = func(cw *csv.Writer) error { return periodCSV(cw, out) }
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
	writeJSON(w, map[string]any{
		"granularity": gran, "days": days, "rows": rows, "unpriced": unpriced,
	})
}

// costByKey values each (model, provider) slice separately and sums the result
// per report row, which is the only correct order: a row mixes models whose
// rates differ by more than an order of magnitude.
//
// A row keeps a cost as soon as ONE of its slices is priced — dropping the
// whole row because a single model is unknown would hide spend that is known.
// The unknown ids are returned so the page can say so, as the reported ids,
// because that is the string a pricing_overrides entry has to match.
func (s *Server) costByKey(n int, at func(int) (string, store.ModelUsageRow)) (map[string]*float64, []string) {
	costs := map[string]*float64{}
	missing := map[string]bool{}
	prices := s.Prices()
	for i := 0; i < n; i++ {
		key, u := at(i)
		cost, ok := prices.Cost(u.Model, time.UnixMilli(u.MinTS),
			u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheCreation, u.Cache1h, u.Cache5m)
		if !ok {
			missing[u.Model] = true
			continue
		}
		if costs[key] == nil {
			costs[key] = new(float64)
		}
		*costs[key] += cost
	}
	out := make([]string, 0, len(missing))
	for m := range missing {
		out = append(out, m)
	}
	sort.Strings(out)
	return costs, out
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// usd renders a cost for CSV. An absent price stays an empty cell: a
// spreadsheet cannot tell 0.00 "free" from 0.00 "unknown", and summing a column
// of the two would quietly understate the total.
func usd(cost *float64) string {
	if cost == nil {
		return ""
	}
	return strconv.FormatFloat(*cost, 'f', 6, 64)
}

func totalsCSV(t store.Totals) []string {
	return []string{i64(t.Events), i64(t.InputTokens), i64(t.OutputTokens),
		i64(t.CacheRead), i64(t.CacheCreation), i64(t.TotalTokens)}
}

var totalsHeader = []string{"events", "input_tokens", "output_tokens",
	"cache_read_tokens", "cache_creation_tokens", "total_tokens"}

func periodCSV(cw *csv.Writer, rows []periodReportRow) error {
	if err := cw.Write(append(append([]string{"bucket"}, totalsHeader...), "cost_usd")); err != nil {
		return err
	}
	for _, r := range rows {
		rec := append([]string{r.Bucket}, totalsCSV(r.Totals)...)
		if err := cw.Write(append(rec, usd(r.CostUSD))); err != nil {
			return err
		}
	}
	return nil
}

func sessionCSV(cw *csv.Writer, rows []sessionReportRow) error {
	head := append([]string{"session_id", "device", "source", "repo", "model", "first_time", "last_time"}, totalsHeader...)
	if err := cw.Write(append(head, "cost_usd")); err != nil {
		return err
	}
	const layout = "2006-01-02 15:04:05"
	for _, r := range rows {
		rec := append([]string{r.SessionID, r.Device, r.Source, r.Repo, r.Model,
			time.UnixMilli(r.FirstTS).Format(layout), time.UnixMilli(r.LastTS).Format(layout)},
			totalsCSV(r.Totals)...)
		if err := cw.Write(append(rec, usd(r.CostUSD))); err != nil {
			return err
		}
	}
	return nil
}
