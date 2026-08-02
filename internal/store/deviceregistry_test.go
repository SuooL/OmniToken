package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suool/omnitoken/internal/model"
)

const (
	testDeviceIDA = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	testDeviceIDB = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a002"
)

func TestOpenMigratesDeviceRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema + quotaSchema + settingsSchema + procSchema); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rows, err := s.db.Query(`PRAGMA table_info(devices)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"device_id",
		"display_name",
		"token_hash",
		"capabilities",
		"created_at",
		"last_seen_at",
		"revoked_at",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("device registry columns = %v, want %v", got, want)
	}

	var tableSQL string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'devices'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{
		"device_id TEXT PRIMARY KEY",
		"display_name TEXT NOT NULL",
		"token_hash TEXT NOT NULL UNIQUE",
		"capabilities TEXT NOT NULL DEFAULT '[]'",
		"created_at INTEGER NOT NULL",
		"last_seen_at INTEGER NOT NULL DEFAULT 0",
		"revoked_at INTEGER NOT NULL DEFAULT 0",
	} {
		if !strings.Contains(tableSQL, constraint) {
			t.Errorf("device registry schema missing constraint %q: %s", constraint, tableSQL)
		}
	}
}

func TestRegisterDeviceHashesTokenAndRoundTripsMetadata(t *testing.T) {
	s := openTestStore(t)
	capabilities := []string{"events", "procs"}
	createdAt := int64(1_722_345_678_901)

	record, err := s.RegisterDevice(testDeviceIDA, "Workstation", "device-secret", capabilities, createdAt)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("device-secret"))
	wantHash := hex.EncodeToString(sum[:])
	if record.DeviceID != testDeviceIDA {
		t.Fatalf("device id = %q, want %q", record.DeviceID, testDeviceIDA)
	}
	if record.DisplayName != "Workstation" {
		t.Fatalf("display name = %q", record.DisplayName)
	}
	if record.TokenHash != wantHash {
		t.Fatalf("token hash = %q, want SHA-256 %q", record.TokenHash, wantHash)
	}
	if !reflect.DeepEqual(record.Capabilities, capabilities) {
		t.Fatalf("capabilities = %v, want %v", record.Capabilities, capabilities)
	}
	if record.CreatedAt != createdAt || record.LastSeenAt != 0 || record.RevokedAt != 0 {
		t.Fatalf("timestamps = created:%d last_seen:%d revoked:%d", record.CreatedAt, record.LastSeenAt, record.RevokedAt)
	}

	var storedHash string
	if err := s.db.QueryRow(`SELECT token_hash FROM devices WHERE device_id = ?`, testDeviceIDA).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != wantHash {
		t.Fatalf("stored token hash = %q, want %q", storedHash, wantHash)
	}
	if storedHash == "device-secret" {
		t.Fatal("plaintext device token was persisted")
	}

	authenticated, ok, err := s.AuthenticateDevice(testDeviceIDA, "device-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("registered device did not authenticate")
	}
	if !reflect.DeepEqual(authenticated, record) {
		t.Fatalf("authenticated record = %#v, want %#v", authenticated, record)
	}
}

func TestRegisterDeviceEnforcesStableUUIDAndUniqueCredential(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDA, "A", "token-a", nil, 10); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RegisterDevice(testDeviceIDA, "renamed", "token-b", nil, 20); err == nil {
		t.Fatal("duplicate device id was accepted")
	}
	if _, err := s.RegisterDevice(testDeviceIDB, "B", "token-a", nil, 20); err == nil {
		t.Fatal("duplicate device credential was accepted")
	}

	for _, invalid := range []string{
		"",
		"not-a-uuid",
		"00000000-0000-0000-0000-000000000000",
		"018F2D5A-7B31-7D98-BF8E-3C2F35A1A003",
	} {
		if _, err := s.RegisterDevice(invalid, "invalid", "unique-"+invalid, nil, 30); err == nil {
			t.Fatalf("invalid device id %q was accepted", invalid)
		}
	}
	if _, err := s.RegisterDevice("018f2d5a-7b31-7d98-bf8e-3c2f35a1a003", "empty", "", nil, 30); err == nil {
		t.Fatal("empty device credential was accepted")
	}
}

func TestUpdateDeviceMetadataPreservesIdentityCredentialAndTimestamps(t *testing.T) {
	s := openTestStore(t)
	createdAt := int64(10)
	if _, err := s.RegisterDevice(testDeviceIDA, "Before", "token-a", []string{"events"}, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchDevice(testDeviceIDA, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(testDeviceIDA, 30); err != nil {
		t.Fatal(err)
	}
	before, err := s.deviceByID(testDeviceIDA)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateDeviceMetadata(testDeviceIDA, "After", []string{"events", "procs"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.deviceByID(testDeviceIDA)
	if err != nil {
		t.Fatal(err)
	}

	if after.DisplayName != "After" || !reflect.DeepEqual(after.Capabilities, []string{"events", "procs"}) {
		t.Fatalf("updated metadata = display:%q capabilities:%v", after.DisplayName, after.Capabilities)
	}
	if after.DeviceID != before.DeviceID ||
		after.TokenHash != before.TokenHash ||
		after.CreatedAt != before.CreatedAt ||
		after.LastSeenAt != before.LastSeenAt ||
		after.RevokedAt != before.RevokedAt {
		t.Fatalf("immutable fields changed: before=%#v after=%#v", before, after)
	}
}

func TestUpdateDeviceMetadataReturnsNotFound(t *testing.T) {
	s := openTestStore(t)
	err := s.UpdateDeviceMetadata(testDeviceIDA, "Missing", []string{"events"})
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("error = %v, want ErrDeviceNotFound", err)
	}
}

func TestSaveHeartbeatKeepsLatestServerReceivedSnapshot(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDA, "Agent", "token-a", []string{"events"}, 10); err != nil {
		t.Fatal(err)
	}
	newer := model.Heartbeat{
		ProtocolVersion: 2,
		DeviceID:        testDeviceIDA,
		BootID:          "018f2d5a-7b31-7d98-bf8e-3c2f35a1c001",
		Sequence:        8,
		SentAt:          9_999_999_999,
		Capabilities:    []string{"events", "heartbeat"},
		QueuedBatches:   3,
		QueuedBytes:     400,
	}
	if err := s.SaveHeartbeat(newer, 2_000); err != nil {
		t.Fatal(err)
	}
	older := newer
	older.Sequence = 7
	older.QueuedBatches = 99
	if err := s.SaveHeartbeat(older, 1_000); err != nil {
		t.Fatal(err)
	}
	got, receivedAt, err := s.LatestHeartbeat(testDeviceIDA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, newer) || receivedAt != 2_000 {
		t.Fatalf("latest heartbeat = %#v at %d, want %#v at 2000", got, receivedAt, newer)
	}
}

func TestListDevicesIncludesRegisteredDevicesWithoutUsage(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDB, "B", "token-b", nil, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterDevice(testDeviceIDA, "A", "token-a", []string{"events"}, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchDevice(testDeviceIDA, 30); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].DeviceID != testDeviceIDA || records[0].LastSeenAt != 30 ||
		records[1].DeviceID != testDeviceIDB {
		t.Fatalf("device records = %#v", records)
	}
}

func TestAuthenticateDeviceRejectsWrongDeviceTokenAndImpersonation(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDA, "A", "token-a", nil, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterDevice(testDeviceIDB, "B", "token-b", nil, 10); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		deviceID string
		token    string
	}{
		{name: "wrong token", deviceID: testDeviceIDA, token: "wrong"},
		{name: "device B token cannot claim device A", deviceID: testDeviceIDA, token: "token-b"},
		{name: "unknown device", deviceID: "018f2d5a-7b31-7d98-bf8e-3c2f35a1a099", token: "token-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := s.AuthenticateDevice(tc.deviceID, tc.token)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("authentication unexpectedly succeeded")
			}
		})
	}
}

func TestRevokedDeviceCannotAuthenticate(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDA, "A", "token-a", nil, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice(testDeviceIDA, 99); err != nil {
		t.Fatal(err)
	}

	record, ok, err := s.AuthenticateDevice(testDeviceIDA, "token-a")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("revoked device authenticated")
	}
	if record.RevokedAt != 99 {
		t.Fatalf("revoked_at = %d, want 99", record.RevokedAt)
	}
}

func TestTouchDeviceUsesServerReceivedMillisecondsWithoutRegression(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RegisterDevice(testDeviceIDA, "A", "token-a", nil, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchDevice(testDeviceIDA, 1_722_345_678_901); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchDevice(testDeviceIDA, 1_722_345_678_000); err != nil {
		t.Fatal(err)
	}

	record, ok, err := s.AuthenticateDevice(testDeviceIDA, "token-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("device did not authenticate")
	}
	if record.LastSeenAt != 1_722_345_678_901 {
		t.Fatalf("last_seen_at = %d, want server received timestamp", record.LastSeenAt)
	}
}
