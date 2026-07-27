package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestDeviceDaily(t *testing.T) {
	s, day1, from, to := seedDeviceStore(t)
	defer s.Close()

	rows, err := s.DeviceDaily(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(rows), rows)
	}
	// Ordered by (day, device): day1 linux/mac, day2 mac/win.
	want := []DeviceDailyRow{
		{Bucket: rows[0].Bucket, Device: "linux", Tokens: 500, Events: 1},
		{Bucket: rows[1].Bucket, Device: "mac", Tokens: 150, Events: 1},
		{Bucket: rows[2].Bucket, Device: "mac", Tokens: 1010, Events: 2},
		{Bucket: rows[3].Bucket, Device: "win", Tokens: 20, Events: 1},
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	if rows[0].Bucket != rows[1].Bucket || rows[2].Bucket != rows[3].Bucket {
		t.Errorf("day grouping wrong: %+v", rows)
	}
	if rows[1].Bucket >= rows[2].Bucket {
		t.Errorf("buckets not ascending: %q, %q", rows[1].Bucket, rows[2].Bucket)
	}

	// Window trims to the second day only.
	day2 := day1.Add(24 * time.Hour)
	rows, err = s.DeviceDaily(day2.Add(-time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Device != "mac" || rows[0].Tokens != 1010 || rows[1].Device != "win" {
		t.Errorf("day2 rows wrong: %+v", rows)
	}
}

func TestDeviceSummary(t *testing.T) {
	s, day1, from, to := seedDeviceStore(t)
	defer s.Close()

	rows, err := s.DeviceSummary(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Busiest first: mac 1160, linux 500, win 20.
	if rows[0].Device != "mac" || rows[1].Device != "linux" || rows[2].Device != "win" {
		t.Fatalf("order wrong: %q %q %q", rows[0].Device, rows[1].Device, rows[2].Device)
	}

	mac := rows[0]
	if mac.TotalTokens != 1160 || mac.Events != 3 {
		t.Errorf("mac totals = %d tokens / %d events, want 1160/3", mac.TotalTokens, mac.Events)
	}
	if mac.InputTokens != 1110 || mac.OutputTokens != 50 {
		t.Errorf("mac split = in %d / out %d, want 1110/50", mac.InputTokens, mac.OutputTokens)
	}
	// Two distinct repos (repoA, repoB); the empty repo of linux must not count.
	if mac.Repos != 2 {
		t.Errorf("mac repos = %d, want 2", mac.Repos)
	}
	if mac.LastTS != day1.Add(25*time.Hour).UnixMilli() {
		t.Errorf("mac last_ts = %d, want %d", mac.LastTS, day1.Add(25*time.Hour).UnixMilli())
	}
	if mac.TopModel != "claude-opus-4" || mac.TopModelTokens != 1150 {
		t.Errorf("mac top model = %q/%d, want claude-opus-4/1150", mac.TopModel, mac.TopModelTokens)
	}
	if len(mac.Models) != 2 || mac.Models[1].Model != "gpt-5" || mac.Models[1].TotalTokens != 10 {
		t.Errorf("mac models wrong: %+v", mac.Models)
	}
	if mac.Models[0].Cache1h != 0 || mac.Models[0].MinTS != day1.UnixMilli() {
		t.Errorf("mac top model cache/min_ts wrong: %+v", mac.Models[0])
	}

	linux := rows[1]
	if linux.Repos != 0 {
		t.Errorf("linux repos = %d, want 0 (empty repo excluded)", linux.Repos)
	}
	if linux.TopModel != "gpt-5" || linux.TotalTokens != 500 {
		t.Errorf("linux row wrong: %+v", linux)
	}

	// Window trims: linux only reported on day 1.
	day2 := day1.Add(24 * time.Hour)
	rows, err = s.DeviceSummary(day2.Add(-time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("day2 rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Device != "mac" || rows[0].TotalTokens != 1010 || rows[0].TopModel != "claude-opus-4" {
		t.Errorf("day2 mac row wrong: %+v", rows[0])
	}
	if rows[1].Device != "win" || rows[1].Repos != 1 {
		t.Errorf("day2 win row wrong: %+v", rows[1])
	}
}

func TestDeviceSummaryEmpty(t *testing.T) {
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	sum, err := s.DeviceSummary(now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatal(err)
	}
	daily, err := s.DeviceDaily(now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 0 || len(daily) != 0 {
		t.Errorf("empty store returned %d summary / %d daily rows", len(sum), len(daily))
	}
}

// seedDeviceStore inserts three devices across two local-time days. Noon
// local timestamps keep the localtime day bucketing deterministic.
func seedDeviceStore(t *testing.T) (s *Store, day1, from, to time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	day1 = time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	day2 := day1.Add(24 * time.Hour)
	events := []model.Event{
		{EventID: "m1", TS: day1.UnixMilli(), Device: "mac", Model: "claude-opus-4",
			Repo: "repoA", InputTokens: 100, OutputTokens: 50},
		{EventID: "m2", TS: day2.UnixMilli(), Device: "mac", Model: "claude-opus-4",
			Repo: "repoB", InputTokens: 1000},
		{EventID: "m3", TS: day2.Add(time.Hour).UnixMilli(), Device: "mac", Model: "gpt-5",
			Repo: "repoA", InputTokens: 10},
		{EventID: "l1", TS: day1.Add(2 * time.Hour).UnixMilli(), Device: "linux", Model: "gpt-5",
			Repo: "", InputTokens: 500},
		{EventID: "w1", TS: day2.Add(2 * time.Hour).UnixMilli(), Device: "win", Model: "gpt-5",
			Repo: "repoC", InputTokens: 20},
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, day1, day1.Add(-time.Hour), day2.Add(3 * time.Hour)
}
