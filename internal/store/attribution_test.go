package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

// Two machines with a synced home directory hold byte-identical logs, so the
// same event_id legitimately arrives from both. Which device column it lands in
// is decided by ADR-0015's rank, and none of it may move a token count.
func TestDeviceAttributionByOrigin(t *testing.T) {
	const id = "cc:msg_01ATTR:req_011Cattr"
	ev := func(device string) model.Event {
		return model.Event{
			EventID: id, TS: time.Now().UnixMilli(), Device: device,
			Source: "claude-code", Model: "claude-opus-4-8",
			InputTokens: 100, OutputTokens: 250, CacheReadTokens: 40,
			CacheCreationTokens: 10,
		}
	}

	cases := []struct {
		name         string
		first        DeviceOrigin
		firstDevice  string
		second       DeviceOrigin
		secondDevice string
		wantDevice   string
		wantOrigin   string
		why          string
	}{
		{
			name: "a self-report corrects a mirror's guess",
			// The SSH pull filed the event under the host it was pulled from,
			// which is only where a copy of the log happens to sit.
			first: OriginObserved, firstDevice: "mbp",
			second: OriginSelf, secondDevice: "macmini",
			wantDevice: "macmini", wantOrigin: "self",
			why: "the machine that ran it outranks the machine that stores a copy",
		},
		{
			name:  "an observer never takes a self-reported row",
			first: OriginSelf, firstDevice: "macmini",
			second: OriginObserved, secondDevice: "mbp",
			wantDevice: "macmini", wantOrigin: "self",
			why: "the overwrite is one-way, exactly like the source promotion",
		},
		{
			name:  "the first self-report wins",
			first: OriginSelf, firstDevice: "macmini",
			second: OriginSelf, secondDevice: "mbp",
			wantDevice: "macmini", wantOrigin: "self",
			why: "whoever ran it reports within seconds; a synced copy shows up later",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "attr.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			now := time.Now().UnixMilli()

			n, err := s.InsertEventsFrom([]model.Event{ev(tc.firstDevice)}, now, tc.first)
			if err != nil || n != 1 {
				t.Fatalf("first insert: n=%d err=%v", n, err)
			}
			n, err = s.InsertEventsFrom([]model.Event{ev(tc.secondDevice)}, now, tc.second)
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("second observation inserted %d rows, want 0 — ADR-0004 dedup still rules", n)
			}

			var (
				rows, input, output, cacheRead, cacheCreate int64
				device, origin                              string
			)
			if err := s.db.QueryRow(
				`SELECT COUNT(*), SUM(input_tokens), SUM(output_tokens),
				        SUM(cache_read_tokens), SUM(cache_creation_tokens)
				 FROM events`).
				Scan(&rows, &input, &output, &cacheRead, &cacheCreate); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("rows = %d, want 1 — one request is one charge", rows)
			}
			// The whole point: attribution moves, counting does not.
			if input != 100 || output != 250 || cacheRead != 40 || cacheCreate != 10 {
				t.Errorf("tokens = %d/%d/%d/%d, want 100/250/40/10 — attribution must never touch a count",
					input, output, cacheRead, cacheCreate)
			}
			if err := s.db.QueryRow(`SELECT device, device_origin FROM events`).Scan(&device, &origin); err != nil {
				t.Fatal(err)
			}
			if device != tc.wantDevice || origin != tc.wantOrigin {
				t.Errorf("device/origin = %q/%q, want %q/%q — %s",
					device, origin, tc.wantDevice, tc.wantOrigin, tc.why)
			}
		})
	}
}

// A self-report with no device name must not blank out an attribution we have.
func TestSelfReportWithoutDeviceNameKeepsAttribution(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "blank.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id = "cc:msg_02ATTR:req_02"
	now := time.Now().UnixMilli()
	base := model.Event{EventID: id, TS: now, Source: "claude-code", OutputTokens: 5}

	withDevice := base
	withDevice.Device = "macmini"
	if _, err := s.InsertEventsFrom([]model.Event{withDevice}, now, OriginObserved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEventsFrom([]model.Event{base}, now, OriginSelf); err != nil {
		t.Fatal(err)
	}
	var device, origin string
	if err := s.db.QueryRow(`SELECT device, device_origin FROM events`).Scan(&device, &origin); err != nil {
		t.Fatal(err)
	}
	if device != "macmini" || origin != "observed" {
		t.Errorf("device/origin = %q/%q, want macmini/observed — a nameless report tells us nothing", device, origin)
	}
}

// InsertEvents is the self-report entry point: agent pushes and proxy traffic
// both describe the reporting machine's own work.
func TestInsertEventsCountsAsSelfReport(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "self.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	if _, err := s.InsertEvents([]model.Event{{
		EventID: "cc:msg_03ATTR:req_03", TS: now, Device: "macmini",
		Source: "claude-code", OutputTokens: 7,
	}}, now); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := s.db.QueryRow(`SELECT device_origin FROM events`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "self" {
		t.Errorf("device_origin = %q, want self", origin)
	}
}

// Databases written before ADR-0015 have no device_origin column. Adding it
// must not disturb their rows, and they must read as 'observed' — the level a
// later self-report is still allowed to correct.
func TestDeviceOriginMigrationDefaultsExistingRowsToObserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	const id = "cc:msg_04ATTR:req_04"

	legacy := strings.Replace(schema, "\tdevice_origin TEXT NOT NULL DEFAULT 'observed',\n", "", 1)
	if legacy == schema {
		t.Fatal("could not build a pre-ADR-0015 schema: the device_origin line moved")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO events (event_id, ts, device, source, output_tokens) VALUES (?,?,?,?,?)`,
		id, time.Now().UnixMilli(), "mbp", "claude-code", 250); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open after migration: %v", err)
	}
	defer s.Close()

	var device, origin string
	var output int64
	if err := s.db.QueryRow(`SELECT device, device_origin, output_tokens FROM events`).
		Scan(&device, &origin, &output); err != nil {
		t.Fatalf("device_origin missing after migration: %v", err)
	}
	if device != "mbp" || output != 250 {
		t.Errorf("migration disturbed the row: device=%q output=%d", device, output)
	}
	if origin != "observed" {
		t.Fatalf("legacy device_origin = %q, want observed", origin)
	}

	// And the correction the default exists for still works.
	now := time.Now().UnixMilli()
	if _, err := s.InsertEventsFrom([]model.Event{{
		EventID: id, TS: now, Device: "macmini", Source: "claude-code", OutputTokens: 250,
	}}, now, OriginSelf); err != nil {
		t.Fatal(err)
	}
	var rows, total int64
	if err := s.db.QueryRow(`SELECT COUNT(*), SUM(output_tokens) FROM events`).Scan(&rows, &total); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || total != 250 {
		t.Errorf("rows/output = %d/%d, want 1/250", rows, total)
	}
	if err := s.db.QueryRow(`SELECT device, device_origin FROM events`).Scan(&device, &origin); err != nil {
		t.Fatal(err)
	}
	if device != "macmini" || origin != "self" {
		t.Errorf("device/origin = %q/%q, want macmini/self — a migrated row must still be correctable", device, origin)
	}
}
