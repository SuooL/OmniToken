package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func reportsJSON(t *testing.T, s *Server, query string) struct {
	Rows []struct {
		Bucket    string   `json:"bucket"`
		SessionID string   `json:"session_id"`
		Total     int64    `json:"total_tokens"`
		CostUSD   *float64 `json:"cost_usd"`
	} `json:"rows"`
	Unpriced []string `json:"unpriced"`
} {
	t.Helper()
	var out struct {
		Rows []struct {
			Bucket    string   `json:"bucket"`
			SessionID string   `json:"session_id"`
			Total     int64    `json:"total_tokens"`
			CostUSD   *float64 `json:"cost_usd"`
		} `json:"rows"`
		Unpriced []string `json:"unpriced"`
	}
	rec := httptest.NewRecorder()
	s.handleReports(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reports?"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// A report row mixes models whose rates differ by more than an order of
// magnitude, so its cost has to be the sum of separately priced models — not
// the row's token total run through any one rate.
func TestReportsCostIsSummedPerModel(t *testing.T) {
	s := newLiveTestServer(t)
	at := insideLookBack()
	ev := func(id, mdl string, out int64) model.Event {
		return model.Event{
			EventID: id, TS: at.UnixMilli(), Device: "mac", Source: "claude-code",
			Model: mdl, Provider: "anthropic-oauth", SessionID: "sess-1",
			InputTokens: 1_000_000, OutputTokens: out,
		}
	}
	if _, err := s.store.InsertEvents([]model.Event{
		ev("a", "claude-opus-4-8", 100_000),
		ev("b", "claude-haiku-4-5", 100_000),
	}, at.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	// Priced independently, then added: Opus input is $5/M and output $25/M,
	// Haiku $1/M and $5/M.
	wantOpus := 1.0*5e-6*1e6 + 0.1*25e-6*1e6
	wantHaiku := 1.0*1e-6*1e6 + 0.1*5e-6*1e6

	got := reportsJSON(t, s, "granularity=daily&days=30")
	if len(got.Rows) != 1 {
		t.Fatalf("rows = %+v, want one day", got.Rows)
	}
	row := got.Rows[0]
	if row.CostUSD == nil {
		t.Fatal("cost_usd missing on a fully priced row")
	}
	if diff := *row.CostUSD - (wantOpus + wantHaiku); diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost_usd = %v, want %v (opus %v + haiku %v)", *row.CostUSD, wantOpus+wantHaiku, wantOpus, wantHaiku)
	}
	if len(got.Unpriced) != 0 {
		t.Errorf("unpriced = %v, want empty", got.Unpriced)
	}

	// The session table answers the same question about the same events.
	sessions := reportsJSON(t, s, "granularity=session&days=30")
	if len(sessions.Rows) != 1 || sessions.Rows[0].CostUSD == nil {
		t.Fatalf("session rows = %+v, want one row carrying a cost", sessions.Rows)
	}
	if diff := *sessions.Rows[0].CostUSD - *row.CostUSD; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("session cost %v != daily cost %v over the same events",
			*sessions.Rows[0].CostUSD, *row.CostUSD)
	}
}

// An unpriced model must not be reported as costing nothing: $0 is a claim, and
// the honest answer is that we do not know.
func TestReportsUnpricedRowHasNoCost(t *testing.T) {
	s := newLiveTestServer(t)
	at := insideLookBack()
	if _, err := s.store.InsertEvents([]model.Event{{
		EventID: "u", TS: at.UnixMilli(), Device: "mac", Source: "codex",
		Model: "no-such-model-v9", Provider: "openai-chatgpt", SessionID: "sess-u",
		InputTokens: 1000, OutputTokens: 500,
	}}, at.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got := reportsJSON(t, s, "granularity=daily&days=30")
	if len(got.Rows) != 1 {
		t.Fatalf("rows = %+v, want one day", got.Rows)
	}
	if got.Rows[0].CostUSD != nil {
		t.Errorf("cost_usd = %v, want absent for a row with no priced model", *got.Rows[0].CostUSD)
	}
	if got.Rows[0].Total != 1500 {
		t.Errorf("total_tokens = %d, want the usage still counted", got.Rows[0].Total)
	}
	if len(got.Unpriced) != 1 || got.Unpriced[0] != "no-such-model-v9" {
		t.Errorf("unpriced = %v, want the reported id named", got.Unpriced)
	}
}

// A row that mixes a priced and an unpriced model keeps the spend we do know.
func TestReportsPartiallyPricedRowKeepsKnownSpend(t *testing.T) {
	s := newLiveTestServer(t)
	at := insideLookBack()
	if _, err := s.store.InsertEvents([]model.Event{
		{EventID: "p", TS: at.UnixMilli(), Device: "mac", Source: "claude-code",
			Model: "claude-opus-4-8", Provider: "anthropic-oauth", InputTokens: 1_000_000},
		{EventID: "q", TS: at.UnixMilli(), Device: "mac", Source: "claude-code",
			Model: "no-such-model-v9", Provider: "anthropic-oauth", InputTokens: 1_000_000},
	}, at.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got := reportsJSON(t, s, "granularity=daily&days=30")
	if len(got.Rows) != 1 || got.Rows[0].CostUSD == nil {
		t.Fatalf("rows = %+v, want the priced half to survive", got.Rows)
	}
	if diff := *got.Rows[0].CostUSD - 5.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost_usd = %v, want 5.0 (1M Opus input at $5/M)", *got.Rows[0].CostUSD)
	}
	if len(got.Unpriced) != 1 {
		t.Errorf("unpriced = %v, want the unknown id surfaced alongside", got.Unpriced)
	}
}

// The CSV export is the same table, so it carries the same last column — and
// leaves the cell empty rather than writing 0, which a spreadsheet would sum.
func TestReportsCSVCarriesCostLast(t *testing.T) {
	s := newLiveTestServer(t)
	at := insideLookBack()
	if _, err := s.store.InsertEvents([]model.Event{
		{EventID: "c", TS: at.UnixMilli(), Device: "mac", Source: "claude-code",
			Model: "claude-opus-4-8", Provider: "anthropic-oauth", SessionID: "s1",
			InputTokens: 1_000_000},
		{EventID: "d", TS: at.Add(time.Minute).UnixMilli(), Device: "mac", Source: "codex",
			Model: "no-such-model-v9", Provider: "openai-chatgpt", SessionID: "s2",
			InputTokens: 10},
	}, at.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	for _, gran := range []string{"daily", "session"} {
		rec := httptest.NewRecorder()
		s.handleReports(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/reports?granularity="+gran+"&days=30&format=csv", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", gran, rec.Code)
		}
		records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("%s: parse csv: %v", gran, err)
		}
		if len(records) < 2 {
			t.Fatalf("%s: csv = %v, want a header and at least one row", gran, records)
		}
		head := records[0]
		if head[len(head)-1] != "cost_usd" {
			t.Errorf("%s: last header = %q, want cost_usd", gran, head[len(head)-1])
		}
		for _, rec := range records[1:] {
			if len(rec) != len(head) {
				t.Fatalf("%s: row %v has %d fields, header has %d", gran, rec, len(rec), len(head))
			}
		}
	}

	// The session split puts the unpriced model in its own row, whose cost cell
	// must be empty rather than "0".
	rec := httptest.NewRecorder()
	s.handleReports(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/reports?granularity=session&days=30&format=csv", nil))
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	var blanks int
	for _, r := range records[1:] {
		if r[len(r)-1] == "" {
			blanks++
		}
	}
	if blanks != 1 {
		t.Errorf("empty cost cells = %d, want exactly the unpriced session: %v", blanks, records)
	}
}
