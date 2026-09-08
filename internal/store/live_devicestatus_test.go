package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// TestDeviceStatusesSplitMatchesCombined pins that the two-query form (ADR-0032)
// returns exactly what the original single full-scan query did: all-time
// last-seen per device, today-bounded token/event totals, most-recent first.
func TestDeviceStatusesSplitMatchesCombined(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local)
	todayStart := time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local)

	ev := func(id, device string, ts time.Time, out int64) model.Event {
		return model.Event{EventID: id, TS: ts.UnixMilli(), Device: device, Source: "codex",
			Model: "gpt-5.6-sol", OutputTokens: out}
	}
	seed := []model.Event{
		// mac: an old event (not today) and two today.
		ev("m-old", "mac", base.AddDate(0, 0, -3), 5),
		ev("m-1", "mac", base.Add(-2*time.Hour), 10),
		ev("m-2", "mac", base.Add(-1*time.Hour), 20), // newest for mac
		// pc: only an old event, nothing today.
		ev("p-old", "pc", base.AddDate(0, 0, -1), 7),
		// srv: one today event, most recent overall.
		ev("s-1", "srv", base.Add(-10*time.Minute), 40),
	}
	if _, err := s.InsertEvents(seed, base.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.DeviceStatuses(todayStart)
	if err != nil {
		t.Fatalf("DeviceStatuses: %v", err)
	}

	want := []DeviceStatus{
		{Device: "srv", LastTS: base.Add(-10 * time.Minute).UnixMilli(), TodayTokens: 40, TodayEvents: 1},
		{Device: "mac", LastTS: base.Add(-1 * time.Hour).UnixMilli(), TodayTokens: 30, TodayEvents: 2},
		{Device: "pc", LastTS: base.AddDate(0, 0, -1).UnixMilli(), TodayTokens: 0, TodayEvents: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// BenchmarkDeviceStatuses measures the split form against the full-table-scan it
// replaced, on a table dominated by history (the shape of a long-lived hub).
// Only a few rows fall in "today"; the old query still had to touch every older
// row's token columns for the today-sum.
func BenchmarkDeviceStatuses(b *testing.B) {
	s, err := Open(b.TempDir() + "/t.db")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local)
	todayStart := time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local)
	const history = 100_000
	batch := make([]model.Event, 0, history+16)
	// The bulk is strictly historical: minutes counting backward from the start
	// of today, so none of it falls in the today window.
	for i := 0; i < history; i++ {
		device := fmt.Sprintf("dev-%d", i%8)
		ts := todayStart.Add(-time.Duration(i+1) * time.Minute)
		batch = append(batch, model.Event{
			EventID: fmt.Sprintf("h-%d", i), TS: ts.UnixMilli(), Device: device,
			Source: "codex", Model: "gpt-5.6-sol", OutputTokens: 10,
		})
	}
	// A thin slice of genuinely-today activity, the realistic shape.
	for i := 0; i < 16; i++ {
		batch = append(batch, model.Event{
			EventID: fmt.Sprintf("t-%d", i), TS: base.Add(-time.Duration(i) * time.Minute).UnixMilli(),
			Device: fmt.Sprintf("dev-%d", i%8), Source: "codex", Model: "gpt-5.6-sol", OutputTokens: 10,
		})
	}
	if _, err := s.InsertEvents(batch, base.UnixMilli()); err != nil {
		b.Fatal(err)
	}

	b.Run("split", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.DeviceStatuses(todayStart); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("combined_fullscan", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := s.rdb.Query(
				`SELECT device, MAX(ts),
				        COALESCE(SUM(CASE WHEN ts >= ?1 THEN input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens ELSE 0 END),0),
				        COALESCE(SUM(CASE WHEN ts >= ?1 THEN 1 ELSE 0 END),0)
				 FROM events GROUP BY device ORDER BY MAX(ts) DESC`,
				todayStart.UnixMilli())
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
	})
}
