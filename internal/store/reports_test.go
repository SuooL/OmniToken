package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mid-month, midday local times so localtime bucketing is TZ-independent
func localTS(y int, mo time.Month, d, h int) time.Time {
	return time.Date(y, mo, d, h, 0, 0, 0, time.Local)
}

func testEvent(id string, ts time.Time, session, device string, in, out int64) model.Event {
	return model.Event{
		EventID: id, TS: ts.UnixMilli(), Device: device, Source: "claude-code",
		Model: "claude-sonnet-4", Repo: "github.com/u/r", SessionID: session,
		InputTokens: in, OutputTokens: out, CacheReadTokens: 5,
	}
}

func mustInsert(t *testing.T, s *Store, events ...model.Event) {
	t.Helper()
	if _, err := s.InsertEvents(events, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestPeriodRowsMonthly(t *testing.T) {
	s := openTestStore(t)
	mustInsert(t, s,
		testEvent("e1", localTS(2026, 3, 10, 12), "s1", "mac", 100, 10),
		testEvent("e2", localTS(2026, 3, 20, 12), "s1", "mac", 200, 20),
		testEvent("e3", localTS(2026, 4, 15, 12), "s2", "mac", 50, 5),
	)
	rows, err := s.PeriodRows("monthly", localTS(2026, 1, 1, 0), localTS(2026, 12, 31, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(rows), rows)
	}
	if rows[0].Bucket != "2026-03" || rows[1].Bucket != "2026-04" {
		t.Errorf("buckets = %q, %q; want 2026-03, 2026-04", rows[0].Bucket, rows[1].Bucket)
	}
	if rows[0].Events != 2 || rows[0].InputTokens != 300 || rows[0].OutputTokens != 30 {
		t.Errorf("march sums wrong: %+v", rows[0])
	}
	if rows[0].TotalTokens != 300+30+2*5 {
		t.Errorf("march total = %d, want %d", rows[0].TotalTokens, 300+30+2*5)
	}
}

func TestPeriodRowsWeekly(t *testing.T) {
	s := openTestStore(t)
	// Wed 2026-03-11 and Thu 2026-03-12 share a week; Wed 2026-03-18 is the next.
	mustInsert(t, s,
		testEvent("e1", localTS(2026, 3, 11, 12), "s1", "mac", 100, 10),
		testEvent("e2", localTS(2026, 3, 12, 12), "s1", "mac", 200, 20),
		testEvent("e3", localTS(2026, 3, 18, 12), "s2", "mac", 50, 5),
	)
	rows, err := s.PeriodRows("weekly", localTS(2026, 3, 1, 0), localTS(2026, 4, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(rows), rows)
	}
	if rows[0].Events != 2 || rows[1].Events != 1 {
		t.Errorf("week event counts = %d, %d; want 2, 1", rows[0].Events, rows[1].Events)
	}
	if rows[0].Bucket != "2026-W10" { // %W: Monday-first week number
		t.Errorf("bucket = %q, want 2026-W10", rows[0].Bucket)
	}
}

func TestPeriodRowsDailyAndUnknown(t *testing.T) {
	s := openTestStore(t)
	mustInsert(t, s, testEvent("e1", localTS(2026, 3, 10, 12), "s1", "mac", 100, 10))
	rows, err := s.PeriodRows("daily", localTS(2026, 3, 1, 0), localTS(2026, 4, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Bucket != "2026-03-10" {
		t.Errorf("daily rows wrong: %+v", rows)
	}
	if _, err := s.PeriodRows("hourly", localTS(2026, 3, 1, 0), localTS(2026, 4, 1, 0)); err == nil {
		t.Error("unknown granularity must error")
	}
}

func TestSessionRows(t *testing.T) {
	s := openTestStore(t)
	t1 := localTS(2026, 3, 10, 10)
	t2 := localTS(2026, 3, 10, 11)
	t3 := localTS(2026, 3, 10, 12)
	mustInsert(t, s,
		testEvent("e1", t1, "sess-a", "mac", 100, 10),
		testEvent("e2", t2, "sess-a", "mac", 200, 20),
		testEvent("e3", t3, "sess-b", "linux", 50, 5),
	)
	rows, err := s.SessionRows(localTS(2026, 3, 1, 0), localTS(2026, 4, 1, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	// ordered by LastTS desc: sess-b first
	if rows[0].SessionID != "sess-b" || rows[0].Device != "linux" {
		t.Errorf("row0 = %+v, want sess-b on linux first", rows[0])
	}
	a := rows[1]
	if a.SessionID != "sess-a" || a.Events != 2 || a.InputTokens != 300 || a.OutputTokens != 30 {
		t.Errorf("sess-a aggregate wrong: %+v", a)
	}
	if a.FirstTS != t1.UnixMilli() || a.LastTS != t2.UnixMilli() {
		t.Errorf("sess-a first/last = %d/%d, want %d/%d", a.FirstTS, a.LastTS, t1.UnixMilli(), t2.UnixMilli())
	}
	if a.Source != "claude-code" || a.Repo != "github.com/u/r" || a.Model != "claude-sonnet-4" {
		t.Errorf("sess-a metadata wrong: %+v", a)
	}

	// limit applies after ordering
	one, err := s.SessionRows(localTS(2026, 3, 1, 0), localTS(2026, 4, 1, 0), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].SessionID != "sess-b" {
		t.Errorf("limit=1 must keep most recent session: %+v", one)
	}
}
