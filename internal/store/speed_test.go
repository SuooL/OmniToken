package store

import (
	"math"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func almost(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// seedSpeedStore inserts events with known per-event speeds:
//   - claude-opus-4 (claude-code): outputs 10..50 over 1000ms → tps 10,20,30,40,50
//   - gpt-5 (codex): must be EXCLUDED from the approx channel — its
//     token_count gaps are a logging artifact, not generation time
//   - noise that must be excluded: output_tokens < 8, duration_ms = 0
//   - claude-opus-4 (proxy): tps 30,60,90 with ttft 300,500,1000
func seedSpeedStore(t *testing.T) (*Store, time.Time, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local)
	ev := func(id string, min int, source, model_ string, out, dur, ttft int64) model.Event {
		return model.Event{EventID: id, TS: base.Add(time.Duration(min) * time.Minute).UnixMilli(),
			Source: source, Model: model_, OutputTokens: out, DurationMS: dur, TTFTMS: ttft}
	}
	events := []model.Event{
		ev("a1", 0, "claude-code", "claude-opus-4", 10, 1000, 0),
		ev("a2", 1, "claude-code", "claude-opus-4", 20, 1000, 0),
		ev("a3", 2, "claude-code", "claude-opus-4", 30, 1000, 0),
		ev("a4", 3, "claude-code", "claude-opus-4", 40, 1000, 0),
		ev("a5", 4, "claude-code", "claude-opus-4", 50, 1000, 0),
		// Even sample count: median must average the two middle ranks.
		ev("g1", 5, "codex", "gpt-5", 100, 10000, 0), // 10 tok/s
		ev("g2", 6, "codex", "gpt-5", 100, 5000, 0),  // 20 tok/s
		ev("g3", 7, "codex", "gpt-5", 100, 2500, 0),  // 40 tok/s
		ev("g4", 8, "codex", "gpt-5", 100, 2000, 0),  // 50 tok/s
		// Excluded: tiny output (noise) and unknown duration.
		ev("x1", 9, "claude-code", "claude-opus-4", 4, 10, 0),
		ev("x2", 10, "codex", "gpt-5", 100, 0, 0),
		// Proxy channel (exact): must never leak into the approx rows.
		ev("p1", 11, "proxy", "claude-opus-4", 30, 1000, 300),  // 30 tok/s
		ev("p2", 12, "proxy", "claude-opus-4", 60, 1000, 500),  // 60 tok/s
		ev("p3", 13, "proxy", "claude-opus-4", 90, 1000, 1000), // 90 tok/s
	}
	if _, err := s.InsertEvents(events, 1); err != nil {
		t.Fatal(err)
	}
	return s, base.Add(-time.Hour), base.Add(time.Hour)
}

func TestSpeedByModelApprox(t *testing.T) {
	s, from, to := seedSpeedStore(t)
	defer s.Close()

	rows, err := s.SpeedByModel(from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	// Codex must not appear: only claude-code feeds the approximate channel.
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (codex must be excluded) (%+v)", len(rows), rows)
	}
	opus := rows[0]
	if opus.Model != "claude-opus-4" {
		t.Fatalf("model = %q, want claude-opus-4", opus.Model)
	}

	// Odd sample count: tps 10,20,30,40,50.
	if opus.Samples != 5 || opus.OutputTokens != 150 {
		t.Errorf("opus samples/output = %d/%d, want 5/150", opus.Samples, opus.OutputTokens)
	}
	almost(t, "opus median", opus.MedianTPS, 30)
	almost(t, "opus avg", opus.AvgTPS, 30)
	almost(t, "opus p90", opus.P90TPS, 50) // nearest rank ceil(0.9*5)=5
	almost(t, "opus min", opus.MinTPS, 10)
	almost(t, "opus max", opus.MaxTPS, 50)
	almost(t, "opus ttft avg (logs are 0)", opus.AvgTTFTMS, 0)
}

func TestSpeedByModelExact(t *testing.T) {
	s, from, to := seedSpeedStore(t)
	defer s.Close()

	rows, err := s.SpeedByModel(from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (only proxy events; %+v)", len(rows), rows)
	}
	p := rows[0]
	if p.Model != "claude-opus-4" || p.Samples != 3 || p.OutputTokens != 180 {
		t.Fatalf("proxy row wrong: %+v", p)
	}
	almost(t, "proxy median", p.MedianTPS, 60)
	almost(t, "proxy avg", p.AvgTPS, 60)
	almost(t, "proxy p90", p.P90TPS, 90)
	almost(t, "proxy ttft avg", p.AvgTTFTMS, 600)
	almost(t, "proxy ttft median", p.MedianTTFTMS, 500)
}

func TestSpeedByModelWindow(t *testing.T) {
	s, from, _ := seedSpeedStore(t)
	defer s.Close()

	// Window ending before any event: no rows, no error.
	rows, err := s.SpeedByModel(from.Add(-2*time.Hour), from, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
}
