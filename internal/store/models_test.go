package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestModelBySource(t *testing.T) {
	s, from, to := seedModelStore(t)
	defer s.Close()

	rows, err := s.ModelBySource(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5: %+v", len(rows), rows)
	}
	// Grouped by model (opus 1600 > gpt-5 550 > tiny-a 30 > tiny-b 20), and
	// within opus by volume (claude-code 1500 > proxy 100).
	gotOrder := [][2]string{}
	for _, r := range rows {
		gotOrder = append(gotOrder, [2]string{r.Model, r.Source})
	}
	wantOrder := [][2]string{
		{"claude-opus-4", "claude-code"},
		{"claude-opus-4", "proxy"},
		{"gpt-5", "codex"},
		{"tiny-a", "proxy"},
		{"tiny-b", "proxy"},
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order[%d] = %v, want %v (all: %v)", i, gotOrder[i], wantOrder[i], gotOrder)
		}
	}

	want := ModelSourceRow{
		Model: "claude-opus-4", Source: "claude-code",
		Totals: Totals{
			Events: 2, InputTokens: 300, OutputTokens: 500,
			CacheRead: 300, CacheCreation: 400, TotalTokens: 1500,
		},
		Cache1h: 100, Cache5m: 50, MinTS: rows[0].MinTS,
	}
	if rows[0] != want {
		t.Errorf("opus/claude-code = %+v, want %+v", rows[0], want)
	}
	if rows[0].MinTS != from.Add(time.Hour).UnixMilli() {
		t.Errorf("MinTS = %d, want earliest event ts %d", rows[0].MinTS, from.Add(time.Hour).UnixMilli())
	}
	if rows[1].TotalTokens != 100 || rows[1].Events != 1 {
		t.Errorf("opus/proxy = %+v, want total 100 / 1 event", rows[1])
	}
}

func TestModelBySourceWindow(t *testing.T) {
	s, from, to := seedModelStore(t)
	defer s.Close()

	day2 := from.Add(24 * time.Hour)
	rows, err := s.ModelBySource(day2, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (day 2 only): %+v", len(rows), rows)
	}
	if rows[0].Model != "claude-opus-4" || rows[0].Source != "claude-code" ||
		rows[0].TotalTokens != 500 || rows[0].Events != 1 {
		t.Errorf("day2 first row = %+v, want opus/claude-code 500 in 1 event", rows[0])
	}
	for _, r := range rows {
		if r.Source == "proxy" && r.Model == "claude-opus-4" {
			t.Errorf("day-1-only row leaked into the day-2 window: %+v", r)
		}
	}
}

func TestModelDailyTopN(t *testing.T) {
	s, from, to := seedModelStore(t)
	defer s.Close()

	rows, err := s.ModelDaily(from, to, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5 (2 on day1, 3 on day2): %+v", len(rows), rows)
	}
	d1, d2 := rows[0].Bucket, rows[2].Bucket
	if d1 >= d2 {
		t.Fatalf("buckets not ascending: %q then %q", d1, d2)
	}

	// Day 1: opus (1000 + 100 across two sources) then gpt-5.
	if rows[0].Model != "claude-opus-4" || rows[0].TotalTokens != 1100 || rows[0].Events != 2 {
		t.Errorf("day1[0] = %+v, want opus 1100 / 2 events", rows[0])
	}
	if rows[1].Model != "gpt-5" || rows[1].TotalTokens != 500 {
		t.Errorf("day1[1] = %+v, want gpt-5 500", rows[1])
	}
	// Day 2: opus, gpt-5, then the merged tail — ModelOther is always last
	// even though it ties gpt-5 on that day's volume.
	if rows[2].Model != "claude-opus-4" || rows[2].TotalTokens != 500 {
		t.Errorf("day2[0] = %+v, want opus 500", rows[2])
	}
	if rows[3].Model != "gpt-5" || rows[3].TotalTokens != 50 {
		t.Errorf("day2[1] = %+v, want gpt-5 50", rows[3])
	}
	other := rows[4]
	if other.Model != ModelOther || other.Bucket != d2 {
		t.Fatalf("day2[2] = %+v, want %q on %s", other, ModelOther, d2)
	}
	// tiny-a (30) + tiny-b (20), one event each.
	if other.TotalTokens != 50 || other.Events != 2 || other.InputTokens != 50 {
		t.Errorf("merged tail = %+v, want total 50 / 2 events / input 50", other)
	}
}

func TestModelDailyNoTailAndDefaults(t *testing.T) {
	s, from, to := seedModelStore(t)
	defer s.Close()

	// topN above the model count keeps every model separate.
	rows, err := s.ModelDaily(from, to, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6 (2 + 4): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Model == ModelOther {
			t.Errorf("unexpected %q bucket with topN=10: %+v", ModelOther, r)
		}
	}

	// topN <= 0 falls back to the default (6), which also covers 4 models.
	def, err := s.ModelDaily(from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != len(rows) {
		t.Errorf("topN=0 rows = %d, want same as topN=10 (%d)", len(def), len(rows))
	}
}

func TestModelDailyWindow(t *testing.T) {
	s, from, to := seedModelStore(t)
	defer s.Close()

	day2 := from.Add(24 * time.Hour)
	rows, err := s.ModelDaily(day2, to, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows in day-2 window")
	}
	bucket := rows[0].Bucket
	for _, r := range rows {
		if r.Bucket != bucket {
			t.Fatalf("window leaked a second day: %q vs %q", r.Bucket, bucket)
		}
	}
	// Within day 2 the leaders are opus (500) and gpt-5 (50); tiny-* merge.
	if len(rows) != 3 || rows[2].Model != ModelOther || rows[2].TotalTokens != 50 {
		t.Errorf("day2 rows = %+v, want opus, gpt-5, %q(50)", rows, ModelOther)
	}
}

// seedModelStore lays down two local-calendar days: one model split across
// two sources, one Codex model, and two long-tail models that exist only to
// exercise the topN merge. Noon local timestamps keep day bucketing stable.
func seedModelStore(t *testing.T) (*Store, time.Time, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 20, 11, 0, 0, 0, time.Local)
	day1 := base.Add(time.Hour)
	day2 := day1.Add(24 * time.Hour)
	events := []model.Event{
		{EventID: "m1", TS: day1.UnixMilli(), Model: "claude-opus-4", Source: "claude-code",
			InputTokens: 100, OutputTokens: 200, CacheReadTokens: 300, CacheCreationTokens: 400,
			Cache1hTokens: 100, Cache5mTokens: 50},
		{EventID: "m2", TS: day1.Add(time.Minute).UnixMilli(), Model: "claude-opus-4", Source: "proxy",
			InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 40},
		{EventID: "m3", TS: day1.Add(2 * time.Minute).UnixMilli(), Model: "gpt-5", Source: "codex",
			InputTokens: 500},
		{EventID: "m4", TS: day2.UnixMilli(), Model: "claude-opus-4", Source: "claude-code",
			InputTokens: 200, OutputTokens: 300},
		{EventID: "m5", TS: day2.Add(time.Minute).UnixMilli(), Model: "gpt-5", Source: "codex",
			OutputTokens: 50},
		{EventID: "m6", TS: day2.Add(2 * time.Minute).UnixMilli(), Model: "tiny-a", Source: "proxy",
			InputTokens: 30},
		{EventID: "m7", TS: day2.Add(3 * time.Minute).UnixMilli(), Model: "tiny-b", Source: "proxy",
			InputTokens: 20},
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, base, day2.Add(time.Hour)
}

// One model reaching us through two channels must not compete with itself for
// a top-N slot. Seen on a real install: the daily chart drew claude-opus-4-8
// and anthropic.claude-opus-4-8 as rival series, each showing part of the
// model's volume, while the chart above it (which folds) showed the whole.
func TestModelDailyFoldsVariantsBeforeTopN(t *testing.T) {
	s, err := Open(t.TempDir() + "/fold.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	ev := func(id, mdl string, out int64) model.Event {
		return model.Event{EventID: id, TS: base.UnixMilli(), Source: "claude-code",
			Model: mdl, OutputTokens: out}
	}
	// Split across channels the model totals 900 — more than the rival's 500 —
	// but each half alone is less.
	if _, err := s.InsertEvents([]model.Event{
		ev("a", "claude-opus-4-8", 500),
		ev("b", "anthropic.claude-opus-4-8", 400),
		ev("c", "gpt-5", 500),
	}, 1); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ModelDaily(base.Add(-time.Hour), base.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	// topN=1: the folded model wins the only slot, gpt-5 falls to the tail.
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want the winner plus a merged tail", rows)
	}
	if rows[0].Model != "claude-opus-4-8" || rows[0].TotalTokens != 900 {
		t.Errorf("top row = %+v, want claude-opus-4-8 with 900", rows[0])
	}
	if rows[1].Model != ModelOther || rows[1].TotalTokens != 500 {
		t.Errorf("tail = %+v, want the 500-token rival", rows[1])
	}
}
