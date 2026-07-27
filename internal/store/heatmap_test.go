package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestHeatmapDays(t *testing.T) {
	s, day1 := seedHeatmapStore(t)
	defer s.Close()

	from := day1.AddDate(0, 0, -1)
	to := day1.AddDate(0, 0, 4)
	rows, err := s.HeatmapDays(from, to)
	if err != nil {
		t.Fatal(err)
	}
	// day2 has no events at all — absent, not a zero row.
	if len(rows) != 2 {
		t.Fatalf("rows = %d (%+v), want 2", len(rows), rows)
	}
	want := []HeatmapDay{
		// day1: (1+2+3+4) + (10+20+30+40) = 10 + 100
		{Bucket: day1.Format("2006-01-02"), Tokens: 110, Events: 2},
		// day3: 100+200+300+400
		{Bucket: day1.AddDate(0, 0, 2).Format("2006-01-02"), Tokens: 1000, Events: 1},
	}
	for i, w := range want {
		if rows[i] != w {
			t.Errorf("rows[%d] = %+v, want %+v", i, rows[i], w)
		}
	}
	if rows[0].Bucket >= rows[1].Bucket {
		t.Errorf("buckets not ascending: %q, %q", rows[0].Bucket, rows[1].Bucket)
	}
}

func TestHeatmapDaysWindow(t *testing.T) {
	s, day1 := seedHeatmapStore(t)
	defer s.Close()

	// [day3, ...) drops day1 entirely: the window is half-open on both ends.
	rows, err := s.HeatmapDays(day1.AddDate(0, 0, 2), day1.AddDate(0, 0, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tokens != 1000 || rows[0].Events != 1 {
		t.Fatalf("trimmed rows = %+v, want only day3", rows)
	}

	// `to` is exclusive: an empty window yields nothing, not day3.
	rows, err = s.HeatmapDays(day1.AddDate(0, 0, 2), day1.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty window returned %+v, want none", rows)
	}

	// A window with no events at all is empty, not an error.
	rows, err = s.HeatmapDays(day1.AddDate(0, 0, 30), day1.AddDate(0, 0, 60))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("far window returned %+v, want none", rows)
	}
}

// seedHeatmapStore writes events on local day1 (x2) and day1+2, leaving day1+1
// empty. Noon-local timestamps keep the localtime day bucketing deterministic
// regardless of the machine's zone.
func seedHeatmapStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	events := []model.Event{
		{EventID: "h1", TS: day1.UnixMilli(),
			InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreationTokens: 4},
		{EventID: "h2", TS: day1.Add(3 * time.Hour).UnixMilli(),
			InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 40},
		{EventID: "h3", TS: day1.AddDate(0, 0, 2).UnixMilli(),
			InputTokens: 100, OutputTokens: 200, CacheReadTokens: 300, CacheCreationTokens: 400},
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, day1
}
