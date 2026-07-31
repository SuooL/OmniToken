package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Device identity merge (ADR-0019). The one overwrite a human has to ask for,
// so the tests here are mostly about what it must NOT do: move a count, drop an
// event, or keep the wrong side of a quota collision.

const (
	hostIdentity  = "JasonHudeMacBook-Pro.local"
	namedIdentity = "suool-mac"
	bystander     = "macmini"
)

// globalCounts is ADR-0019 §3 written out literally: whole-database totals with
// no device filter at all. The merge must leave every one of these untouched —
// "equal", not "smaller". A shrinking event count means someone wrote a DELETE.
type globalCounts struct {
	Rows          int64
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Cache1h       int64
	Cache5m       int64
	DistinctIDs   int64
}

func readGlobalCounts(t *testing.T, s *Store) globalCounts {
	t.Helper()
	var g globalCounts
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
	       COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0),
	       COALESCE(SUM(cache_1h_tokens),0), COALESCE(SUM(cache_5m_tokens),0)
	 FROM events`).Scan(&g.Rows, &g.Input, &g.Output, &g.CacheRead, &g.CacheCreation, &g.Cache1h, &g.Cache5m)
	if err != nil {
		t.Fatalf("global counts: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT event_id) FROM events`).Scan(&g.DistinctIDs); err != nil {
		t.Fatalf("distinct event ids: %v", err)
	}
	return g
}

func countRows(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "merge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mergeEvent(id, device string, ts int64) model.Event {
	return model.Event{
		EventID: id, TS: ts, Device: device,
		Source: "claude-code", Model: "claude-opus-4-8",
		InputTokens: 100, OutputTokens: 250, CacheReadTokens: 40,
		CacheCreationTokens: 10, Cache1hTokens: 7, Cache5mTokens: 3,
	}
}

// seedTwoIdentities fills a database that looks like the real one: one machine
// that landed under two names, plus a third machine that must not be touched.
func seedTwoIdentities(t *testing.T, s *Store) {
	t.Helper()
	base := time.Date(2026, 7, 31, 0, 0, 12, 0, time.UTC).UnixMilli()
	events := []model.Event{
		mergeEvent("cc:msg_host:req_host", hostIdentity, base),
		mergeEvent("cc:msg_named1:req_named1", namedIdentity, base-86_400_000),
		mergeEvent("cc:msg_named2:req_named2", namedIdentity, base+1000),
		mergeEvent("cc:msg_other:req_other", bystander, base+2000),
	}
	if n, err := s.InsertEventsFrom(events, base, OriginSelf); err != nil || n != len(events) {
		t.Fatalf("seed events: n=%d err=%v", n, err)
	}
	quotas := []model.QuotaSnapshot{
		// Same window observed by both identities at the same instant: the
		// collision ADR-0019 §4 says is inevitable on one machine.
		{Device: hostIdentity, Source: "codex", LimitID: "primary", Scope: "primary",
			WindowMinutes: 300, UsedPercent: 11, ObservedAt: base},
		{Device: namedIdentity, Source: "codex", LimitID: "primary", Scope: "primary",
			WindowMinutes: 300, UsedPercent: 22, ObservedAt: base},
		// Only the host identity saw this one; it must survive the merge.
		{Device: hostIdentity, Source: "claude-code", LimitID: "statusline", Scope: "five_hour",
			WindowMinutes: 300, UsedPercent: 33, ObservedAt: base + 5000},
		{Device: bystander, Source: "codex", LimitID: "primary", Scope: "primary",
			WindowMinutes: 300, UsedPercent: 44, ObservedAt: base},
	}
	if _, err := s.InsertQuotas(quotas); err != nil {
		t.Fatalf("seed quotas: %v", err)
	}
	for _, r := range []model.ProcReport{
		{Device: hostIdentity, ObservedAt: base, Sessions: []model.ProcSession{{PID: 4242, Source: "codex"}}},
		{Device: namedIdentity, ObservedAt: base, Sessions: []model.ProcSession{{PID: 4242, Source: "claude-code"}}},
		{Device: bystander, ObservedAt: base, Sessions: []model.ProcSession{{PID: 99, Source: "codex"}}},
	} {
		if _, err := s.ApplyProcReport(r); err != nil {
			t.Fatalf("seed procs: %v", err)
		}
	}
}

// The hard one (ADR-0019 §3). Nothing about how much was used may move.
func TestDeviceMergeKeepsEveryCountIdentical(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)

	before := readGlobalCounts(t, s)
	hostEvents := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, hostIdentity)
	namedEvents := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, namedIdentity)

	applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	after := readGlobalCounts(t, s)
	if after != before {
		t.Fatalf("merge moved a count: before=%+v after=%+v — ADR-0019 §3 says these are equal, not smaller", before, after)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, hostIdentity); got != 0 {
		t.Fatalf("%d events still filed under the merged-away identity", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, namedIdentity); got != hostEvents+namedEvents {
		t.Fatalf("target holds %d events, want %d", got, hostEvents+namedEvents)
	}
	if applied.EventsMoved != hostEvents {
		t.Fatalf("reported %d events moved, want %d", applied.EventsMoved, hostEvents)
	}
	// The bystander is a different machine and was never part of this.
	if got := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, bystander); got != 1 {
		t.Fatalf("bystander device lost rows: %d", got)
	}
}

// The reversed-direction trap of ADR-0019 §4: SQLite's `UPDATE OR REPLACE`
// deletes the row that already exists (the target) and keeps the one being
// updated (the source) — the opposite of what this merge must do.
func TestDeviceMergeKeepsTargetRowOnQuotaCollision(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)
	base := time.Date(2026, 7, 31, 0, 0, 12, 0, time.UTC).UnixMilli()

	applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	var used float64
	err = s.db.QueryRow(`SELECT used_percent FROM quota_snapshots
	 WHERE device = ? AND source = 'codex' AND scope = 'primary' AND window_minutes = 300 AND observed_at = ?`,
		namedIdentity, base).Scan(&used)
	if err != nil {
		t.Fatalf("collided quota row: %v", err)
	}
	if used != 22 {
		t.Fatalf("collision kept used_percent=%v, want 22 (the target's row) — the drop went the wrong way", used)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, hostIdentity); got != 0 {
		t.Fatalf("%d quota rows still under the merged-away identity", got)
	}
	// The non-colliding snapshot moved across; only the duplicate observation went.
	if got := countRows(t, s, `SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, namedIdentity); got != 2 {
		t.Fatalf("target has %d quota rows, want 2", got)
	}
	if applied.QuotaDropped != 1 || applied.QuotaMoved != 1 {
		t.Fatalf("quota accounting: moved=%d dropped=%d, want 1 and 1", applied.QuotaMoved, applied.QuotaDropped)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, bystander); got != 1 {
		t.Fatalf("bystander quota rows changed: %d", got)
	}
}

// Instantaneous state is dropped, not migrated (ADR-0019 §4): the primary key
// contains the device, both identities share one PID space, and the next report
// rewrites the whole thing within seconds anyway.
func TestDeviceMergeDropsLiveRowsInsteadOfMigrating(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)

	applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM live_sessions WHERE device = ?`, hostIdentity); got != 0 {
		t.Fatalf("%d live sessions left behind", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM live_reports WHERE device = ?`, hostIdentity); got != 0 {
		t.Fatalf("%d live reports left behind", got)
	}
	// The target keeps exactly its own row: a migrated PID 4242 would have
	// overwritten it or collided.
	var source string
	if err := s.db.QueryRow(`SELECT source FROM live_sessions WHERE device = ? AND pid = 4242`,
		namedIdentity).Scan(&source); err != nil {
		t.Fatalf("target live session: %v", err)
	}
	if source != "claude-code" {
		t.Fatalf("target live session now reads %q — the source device's row was migrated over it", source)
	}
	if applied.LiveRowsDropped != 2 {
		t.Fatalf("reported %d live rows dropped, want 2 (one session + one report)", applied.LiveRowsDropped)
	}
}

// A v2 device files its events under the registry's UUID, so a merge really can
// have a UUID on one side and a hand-typed name on the other — the operation
// only ever compares the literal `device` string.
//
// What it must not do is follow that UUID into the registry: `devices` is a
// credential table (one machine enrolled twice is two credentials) and
// `ingest_receipts` is the replay guard, which reports a conflict the moment a
// batch's device_id stops matching its receipt. Both stay put (ADR-0019 §2).
func TestDeviceMergeLeavesRegistryAndReceiptsAlone(t *testing.T) {
	const enrolledID = "6f1d0d1e-0a5c-4c3f-9f2f-0b7c9d5a1e42"
	s := openStore(t)
	seedTwoIdentities(t, s)
	now := time.Now().UnixMilli()
	if _, err := s.RegisterDevice(enrolledID, "老名字", "plaintext-token", []string{"events"}, now); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:v2:req", enrolledID, now)}, now, OriginSelf); err != nil {
		t.Fatalf("seed v2 event: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO ingest_receipts
	 (batch_id, device_id, protocol_version, ack_sequence, accepted, duplicates, server_time)
	 VALUES ('batch-1', ?, 2, 'seq-1', 3, 0, ?)`, enrolledID, now); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	if _, err := s.MergeDeviceIdentity(enrolledID, namedIdentity, "admin", now); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if got := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, enrolledID); got != 0 {
		t.Fatalf("%d events still filed under the enrolled UUID", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM devices WHERE device_id = ?`, enrolledID); got != 1 {
		t.Fatalf("registry row for the merged-away identity: %d, want it untouched", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM ingest_receipts WHERE device_id = ?`, enrolledID); got != 1 {
		t.Fatalf("ingest receipt was rewritten: %d rows left under the old device_id", got)
	}
}

// device_origin records how the device value was obtained, and the merge did
// not change that fact (ADR-0019 §2).
func TestDeviceMergeLeavesDeviceOriginAlone(t *testing.T) {
	s := openStore(t)
	base := time.Now().UnixMilli()
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:obs:req", hostIdentity, base)},
		base, OriginObserved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:self:req", namedIdentity, base)},
		base, OriginSelf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", base); err != nil {
		t.Fatalf("merge: %v", err)
	}
	var origin string
	if err := s.db.QueryRow(`SELECT device_origin FROM events WHERE event_id = 'cc:obs:req'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != string(OriginObserved) {
		t.Fatalf("device_origin became %q; the merge did not change how that row's device was obtained", origin)
	}
}

func TestDeviceMergeMovesDisplayNameWithoutOverwritingTheTargets(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   map[string]string
		moved  bool
	}{
		{
			name:   "only the source has a display name",
			labels: map[string]string{hostIdentity: "旧 MacBook", bystander: "台式机"},
			want:   map[string]string{namedIdentity: "旧 MacBook", bystander: "台式机"},
			moved:  true,
		},
		{
			name:   "both have one: the target's stands",
			labels: map[string]string{hostIdentity: "旧 MacBook", namedIdentity: "主力机"},
			want:   map[string]string{namedIdentity: "主力机"},
			moved:  false,
		},
		{
			name:   "neither has one",
			labels: map[string]string{bystander: "台式机"},
			want:   map[string]string{bystander: "台式机"},
			moved:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openStore(t)
			seedTwoIdentities(t, s)
			if err := s.SetSettingsJSON(DeviceLabelsKey, tc.labels); err != nil {
				t.Fatal(err)
			}
			applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli())
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			got := map[string]string{}
			if err := s.GetSettingsJSON(DeviceLabelsKey, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("labels = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("labels = %v, want %v", got, tc.want)
				}
			}
			if applied.LabelMoved != tc.moved {
				t.Fatalf("LabelMoved = %v, want %v", applied.LabelMoved, tc.moved)
			}
		})
	}
}

func TestDeviceMergeRefusesNonsenseArguments(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)
	cases := []struct {
		name     string
		from, to string
		why      string
	}{
		{"same identity", namedIdentity, namedIdentity, "merging a name into itself can only be a mis-click"},
		{"unknown source", "typo-mac", namedIdentity, "a typo would otherwise report a successful merge of nothing"},
		{"unknown target", hostIdentity, "typo-mac", "the same typo, in the direction that would strand the data"},
		{"empty source", "", namedIdentity, "the empty device name is not an identity"},
		{"empty target", hostIdentity, "", "merging into nothing would hide the rows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := readGlobalCounts(t, s)
			if _, err := s.MergeDeviceIdentity(tc.from, tc.to, "admin", time.Now().UnixMilli()); err == nil {
				t.Fatalf("merge(%q → %q) succeeded; %s", tc.from, tc.to, tc.why)
			}
			if after := readGlobalCounts(t, s); after != before {
				t.Fatalf("a rejected merge still changed the database: %+v → %+v", before, after)
			}
		})
	}
}

// The preview a user decides on must come from the same code as the statements
// that run (ADR-0019 §6.2).
func TestDeviceMergePreviewMatchesWhatTheMergeDoes(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)

	plan, err := s.PlanDeviceMerge(hostIdentity, namedIdentity)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	before := readGlobalCounts(t, s)
	applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if plan.EventsMoved != applied.EventsMoved || plan.QuotaMoved != applied.QuotaMoved ||
		plan.QuotaDropped != applied.QuotaDropped || plan.LiveRowsDropped != applied.LiveRowsDropped {
		t.Fatalf("preview %+v did not describe what happened %+v", plan, applied)
	}
	if plan.From.Events != 1 || plan.To.Events != 2 {
		t.Fatalf("preview event counts wrong: from=%d to=%d", plan.From.Events, plan.To.Events)
	}
	if plan.From.TotalTokens == 0 || plan.To.TotalTokens == 0 {
		t.Fatal("preview must show each identity's volume so the user can tell them apart")
	}
	if plan.From.FirstTS == 0 || plan.From.LastTS == 0 {
		t.Fatal("preview must show each identity's first and last event time")
	}
	// A preview changes nothing.
	if after := readGlobalCounts(t, s); after != before {
		t.Fatalf("the plan itself mutated the database: %+v → %+v", before, after)
	}
}

// The merge is unrecoverable, so the record of it is the only thing a user has
// afterwards to check what was touched (ADR-0019 §5).
func TestDeviceMergeAppendsAnAuditRecord(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)
	at := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC).UnixMilli()

	applied, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", at)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	history, err := s.DeviceMergeHistory()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d records, want 1", len(history))
	}
	got := history[0]
	want := DeviceMergeRecord{
		From: hostIdentity, To: namedIdentity, At: at, Actor: "admin",
		Events: applied.EventsMoved, QuotaSnapshots: applied.QuotaMoved,
		QuotaDropped: applied.QuotaDropped, LiveRowsDropped: applied.LiveRowsDropped,
	}
	if got != want {
		t.Fatalf("audit record = %+v, want %+v", got, want)
	}

	// A second merge appends rather than replaces.
	if _, err := s.MergeDeviceIdentity(bystander, namedIdentity, "admin", at+1000); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	history, err = s.DeviceMergeHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].From != bystander {
		t.Fatalf("history = %+v, want the second merge appended", history)
	}
}

// One transaction, or nothing (ADR-0019 §2). A corrupt audit document is the
// one failure that is easy to provoke on demand; any failure has to leave the
// database exactly as it was, with no half-merged identity.
func TestDeviceMergeRollsBackEverythingOnFailure(t *testing.T) {
	s := openStore(t)
	seedTwoIdentities(t, s)
	if err := s.SetSetting(DeviceMergesKey, "{ not a json array"); err != nil {
		t.Fatal(err)
	}
	before := readGlobalCounts(t, s)
	hostEvents := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, hostIdentity)
	hostQuotas := countRows(t, s, `SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, hostIdentity)
	hostLive := countRows(t, s, `SELECT COUNT(*) FROM live_sessions WHERE device = ?`, hostIdentity)

	if _, err := s.MergeDeviceIdentity(hostIdentity, namedIdentity, "admin", time.Now().UnixMilli()); err == nil {
		t.Fatal("merge succeeded despite an unreadable audit log; the record is not optional")
	}
	if after := readGlobalCounts(t, s); after != before {
		t.Fatalf("counts moved during a failed merge: %+v → %+v", before, after)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM events WHERE device = ?`, hostIdentity); got != hostEvents {
		t.Fatalf("events half-merged: %d of %d left", got, hostEvents)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM quota_snapshots WHERE device = ?`, hostIdentity); got != hostQuotas {
		t.Fatalf("quota rows half-merged: %d of %d left", got, hostQuotas)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM live_sessions WHERE device = ?`, hostIdentity); got != hostLive {
		t.Fatalf("live rows half-deleted: %d of %d left", got, hostLive)
	}
}

// The startup hint of ADR-0019 §7.3 rests on one fact and no heuristic: both of
// this machine's candidate names have self-reported into the same database.
func TestSelfReportedDevicesListsOnlySelfAttributedNames(t *testing.T) {
	s := openStore(t)
	base := time.Now().UnixMilli()
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:a:req", hostIdentity, base)},
		base, OriginSelf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:b:req", namedIdentity, base)},
		base, OriginSelf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEventsFrom([]model.Event{mergeEvent("cc:c:req", bystander, base)},
		base, OriginObserved); err != nil {
		t.Fatal(err)
	}
	names, err := s.SelfReportedDevices()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{hostIdentity: true, namedIdentity: true}
	if len(names) != len(want) {
		t.Fatalf("self-reported devices = %v, want %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("self-reported devices = %v, want only %v — an observed row is not a self-report", names, want)
		}
	}
}
