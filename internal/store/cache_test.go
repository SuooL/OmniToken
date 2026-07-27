package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestCacheByModel(t *testing.T) {
	s, from, to := seedCacheStore(t)
	defer s.Close()

	rows, err := s.CacheByModel(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Ordered by cache traffic: opus (2 events) first.
	opus, gpt := rows[0], rows[1]
	if opus.Model != "claude-opus-4" || gpt.Model != "gpt-5" {
		t.Fatalf("order/models wrong: %q, %q", opus.Model, gpt.Model)
	}
	want := CacheModelRow{
		Model: "claude-opus-4", Events: 2, Input: 300, CacheRead: 3000,
		CacheCreation: 900, Cache1h: 600, Cache5m: 300, MinTS: opus.MinTS,
	}
	if opus != want {
		t.Errorf("opus row = %+v, want %+v", opus, want)
	}
	if opus.MinTS == 0 {
		t.Error("opus MinTS not populated")
	}
	if gpt.Events != 1 || gpt.Input != 500 || gpt.CacheRead != 100 || gpt.CacheCreation != 0 {
		t.Errorf("gpt row wrong: %+v", gpt)
	}

	// Window trims: second day only.
	day2 := from.Add(24 * time.Hour)
	rows, err = s.CacheByModel(day2, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "claude-opus-4" || rows[0].Events != 1 || rows[0].CacheRead != 2000 {
		t.Errorf("day2 rows wrong: %+v", rows)
	}
}

func TestCacheDaily(t *testing.T) {
	s, from, to := seedCacheStore(t)
	defer s.Close()

	rows, err := s.CacheDaily(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	d1, d2 := rows[0], rows[1]
	if d1.Bucket >= d2.Bucket {
		t.Errorf("buckets not ascending: %q, %q", d1.Bucket, d2.Bucket)
	}
	// Day 1: opus(100 in, 1000 read) + gpt(500 in, 100 read).
	if d1.Input != 600 || d1.CacheRead != 1100 {
		t.Errorf("day1 = %+v, want input 600, cache_read 1100", d1)
	}
	// Day 2: opus(200 in, 2000 read).
	if d2.Input != 200 || d2.CacheRead != 2000 {
		t.Errorf("day2 = %+v, want input 200, cache_read 2000", d2)
	}
}

// seedCacheStore inserts two models across two local-time days. Noon local
// timestamps keep the localtime day bucketing deterministic.
func seedCacheStore(t *testing.T) (*Store, time.Time, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	day2 := day1.Add(24 * time.Hour)
	events := []model.Event{
		{EventID: "c1", TS: day1.UnixMilli(), Model: "claude-opus-4",
			InputTokens: 100, CacheReadTokens: 1000, CacheCreationTokens: 300,
			Cache1hTokens: 200, Cache5mTokens: 100},
		{EventID: "c2", TS: day2.UnixMilli(), Model: "claude-opus-4",
			InputTokens: 200, CacheReadTokens: 2000, CacheCreationTokens: 600,
			Cache1hTokens: 400, Cache5mTokens: 200},
		{EventID: "g1", TS: day1.Add(time.Hour).UnixMilli(), Model: "gpt-5",
			InputTokens: 500, CacheReadTokens: 100},
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, day1.Add(-time.Hour), day2.Add(time.Hour)
}
