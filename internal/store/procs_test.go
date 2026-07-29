package store

import (
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func openProcStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/procs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A report is a snapshot of one machine: what it lists is what is running.
// Anything the previous report had and this one does not has exited.
func TestProcReportReplacesDeviceState(t *testing.T) {
	s := openProcStore(t)
	t0 := time.Now()

	changed, err := s.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: t0.UnixMilli(),
		Sessions: []model.ProcSession{
			{PID: 100, Source: "claude-code", StartedAt: t0.Add(-time.Hour).UnixMilli()},
			{PID: 200, Source: "codex", StartedAt: t0.Add(-time.Minute).UnixMilli()},
		},
	})
	if err != nil || !changed {
		t.Fatalf("first report: changed=%v err=%v, want true/nil", changed, err)
	}

	// Same set again: still running, nothing to broadcast.
	changed, err = s.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: t0.Add(15 * time.Second).UnixMilli(),
		Sessions: []model.ProcSession{
			{PID: 100, Source: "claude-code", StartedAt: t0.Add(-time.Hour).UnixMilli()},
			{PID: 200, Source: "codex", StartedAt: t0.Add(-time.Minute).UnixMilli()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("unchanged process set reported as changed; that would broadcast every tick")
	}

	// Codex exits.
	changed, err = s.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: t0.Add(30 * time.Second).UnixMilli(),
		Sessions: []model.ProcSession{
			{PID: 100, Source: "claude-code", StartedAt: t0.Add(-time.Hour).UnixMilli()},
		},
	})
	if err != nil || !changed {
		t.Fatalf("exit report: changed=%v err=%v, want true/nil", changed, err)
	}
	running, err := s.RunningSessions(t0.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].PID != 100 {
		t.Fatalf("running = %+v, want only pid 100", running)
	}
}

// A retry can arrive after a newer report. Applying it would bring exited
// processes back to life.
func TestProcReportIgnoresOutOfOrder(t *testing.T) {
	s := openProcStore(t)
	now := time.Now()
	if _, err := s.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: now.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ApplyProcReport(model.ProcReport{
		Device: "mac", ObservedAt: now.Add(-time.Minute).UnixMilli(),
		Sessions: []model.ProcSession{{PID: 100, Source: "codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("stale report was applied")
	}
	running, err := s.RunningSessions(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Fatalf("running = %+v, want none", running)
	}
}

// The distinction the panel depends on: a device that reports nothing running
// is not the same as a device that never reports (ADR-0012).
func TestEmptyReportStillCountsAsReporting(t *testing.T) {
	s := openProcStore(t)
	now := time.Now()
	if _, err := s.ApplyProcReport(model.ProcReport{Device: "mac", ObservedAt: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	reporters, err := s.ProcReporters(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 1 || reporters[0].Device != "mac" {
		t.Fatalf("reporters = %+v, want [mac]", reporters)
	}
}

// Nobody deletes an offline machine's rows — the reader ages them out, because
// the only party that could clean up is the machine that went away.
func TestStaleDeviceDropsOutByTTL(t *testing.T) {
	s := openProcStore(t)
	old := time.Now().Add(-10 * time.Minute)
	if _, err := s.ApplyProcReport(model.ProcReport{
		Device: "laptop", ObservedAt: old.UnixMilli(),
		Sessions: []model.ProcSession{{PID: 7, Source: "claude-code"}},
	}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-90 * time.Second)
	running, err := s.RunningSessions(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Errorf("running = %+v, want none past the TTL", running)
	}
	reporters, err := s.ProcReporters(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(reporters) != 0 {
		t.Errorf("reporters = %+v, want none past the TTL", reporters)
	}
	// Still there before the cutoff: the data is aged out, not deleted.
	running, err = s.RunningSessions(old.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Errorf("running = %+v, want the row to still exist", running)
	}
}

// Two machines can each run a PID 100; neither may overwrite the other.
func TestProcReportsAreScopedPerDevice(t *testing.T) {
	s := openProcStore(t)
	now := time.Now()
	for _, dev := range []string{"mac", "linux-box"} {
		if _, err := s.ApplyProcReport(model.ProcReport{
			Device: dev, ObservedAt: now.UnixMilli(),
			Sessions: []model.ProcSession{{PID: 100, Source: "claude-code"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	running, err := s.RunningSessions(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 {
		t.Fatalf("running = %+v, want one row per device", running)
	}
}
