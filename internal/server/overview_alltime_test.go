package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

// TestAllTimeOverviewCachedAcrossTTL pins ADR-0031: the two unbounded lifetime
// panels are recomputed at most once per allTimeCacheTTL, while the windowed
// panels stay fresh on every call. It also guards the refactor's correctness —
// a stale all-time total must still be a correct one, just older.
func TestAllTimeOverviewCachedAcrossTTL(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	prices, err := pricing.Load(nil)
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}

	clk := time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return clk }
	s := &Server{cfg: &Config{}, store: st, prices: prices, bcast: newBroadcaster(),
		now: nowFn, readCache: newRespCache(nowFn)}

	out := func(resp map[string]any) (allTime, today int64) {
		return resp["all_time"].(store.Totals).OutputTokens,
			resp["today"].(store.Totals).OutputTokens
	}
	insert := func(id string, output int64) {
		ev := model.Event{EventID: id, TS: clk.UnixMilli(), Device: "mac", Source: "codex",
			Model: "gpt-5.6-sol", Provider: "custom", OutputTokens: output}
		if _, err := st.InsertEvents([]model.Event{ev}, clk.UnixMilli()); err != nil {
			t.Fatalf("InsertEvents %s: %v", id, err)
		}
	}

	insert("a", 100)
	r1, err := s.computeOverview(30)
	if err != nil {
		t.Fatalf("computeOverview 1: %v", err)
	}
	if all, today := out(r1); all != 100 || today != 100 {
		t.Fatalf("first pass all_time=%d today=%d, want 100/100", all, today)
	}

	// New usage lands; only a fraction of allTimeCacheTTL passes.
	insert("b", 200)
	clk = clk.Add(allTimeCacheTTL / 2)
	r2, err := s.computeOverview(30)
	if err != nil {
		t.Fatalf("computeOverview 2: %v", err)
	}
	if all, today := out(r2); all != 100 || today != 300 {
		t.Fatalf("within TTL all_time=%d today=%d, want 100 (cached) / 300 (fresh)", all, today)
	}

	// Past the TTL the lifetime total catches up.
	clk = clk.Add(allTimeCacheTTL)
	r3, err := s.computeOverview(30)
	if err != nil {
		t.Fatalf("computeOverview 3: %v", err)
	}
	if all, today := out(r3); all != 300 || today != 300 {
		t.Fatalf("after TTL all_time=%d today=%d, want 300/300 (recomputed)", all, today)
	}
}
