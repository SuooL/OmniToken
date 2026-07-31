package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suool/omnitoken/internal/model"
)

const deviceRegistrySchema = `
CREATE TABLE IF NOT EXISTS devices (
	device_id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	capabilities TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS device_heartbeats (
	device_id TEXT PRIMARY KEY,
	heartbeat TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	FOREIGN KEY(device_id) REFERENCES devices(device_id)
);
`

var ErrDeviceNotFound = errors.New("device not found")

// DeviceRecord is the persisted identity and mutable metadata for one agent.
// TokenHash is a lowercase SHA-256 hex digest; plaintext credentials are never
// stored.
type DeviceRecord struct {
	DeviceID     string   `json:"device_id"`
	DisplayName  string   `json:"display_name"`
	TokenHash    string   `json:"-"`
	Capabilities []string `json:"capabilities"`
	CreatedAt    int64    `json:"created_at"`
	LastSeenAt   int64    `json:"last_seen_at"`
	RevokedAt    int64    `json:"revoked_at"`
}

func (s *Store) DeviceByID(deviceID string) (DeviceRecord, error) {
	record, err := s.deviceByID(deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceRecord{}, ErrDeviceNotFound
	}
	return record, err
}

func (s *Store) ListDevices() ([]DeviceRecord, error) {
	rows, err := s.db.Query(`
		SELECT device_id, display_name, token_hash, capabilities,
		       created_at, last_seen_at, revoked_at
		FROM devices
		ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []DeviceRecord
	for rows.Next() {
		var record DeviceRecord
		var capabilitiesJSON string
		if err := rows.Scan(
			&record.DeviceID,
			&record.DisplayName,
			&record.TokenHash,
			&capabilitiesJSON,
			&record.CreatedAt,
			&record.LastSeenAt,
			&record.RevokedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capabilitiesJSON), &record.Capabilities); err != nil {
			return nil, fmt.Errorf("decode device capabilities: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// SaveHeartbeat coalesces latest-only agent state by server receipt time.
// Client SentAt remains diagnostic and cannot displace a newer server receipt.
func (s *Store) SaveHeartbeat(heartbeat model.Heartbeat, receivedAt int64) error {
	encoded, err := json.Marshal(heartbeat)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO device_heartbeats(device_id, heartbeat, received_at)
		VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			heartbeat = excluded.heartbeat,
			received_at = excluded.received_at
		WHERE excluded.received_at >= device_heartbeats.received_at`,
		heartbeat.DeviceID, string(encoded), receivedAt,
	)
	return err
}

func (s *Store) LatestHeartbeat(deviceID string) (model.Heartbeat, int64, error) {
	var encoded string
	var receivedAt int64
	if err := s.db.QueryRow(
		`SELECT heartbeat, received_at FROM device_heartbeats WHERE device_id = ?`,
		deviceID,
	).Scan(&encoded, &receivedAt); err != nil {
		return model.Heartbeat{}, 0, err
	}
	var heartbeat model.Heartbeat
	if err := json.Unmarshal([]byte(encoded), &heartbeat); err != nil {
		return model.Heartbeat{}, 0, fmt.Errorf("decode heartbeat: %w", err)
	}
	return heartbeat, receivedAt, nil
}

// RegisterDevice creates a stable device principal. Re-registering a device ID
// or credential is rejected by the database uniqueness constraints.
func (s *Store) RegisterDevice(deviceID, displayName, plaintextToken string, capabilities []string, createdAt int64) (DeviceRecord, error) {
	if !isCanonicalUUID(deviceID) {
		return DeviceRecord{}, fmt.Errorf("invalid device id %q", deviceID)
	}
	if plaintextToken == "" {
		return DeviceRecord{}, errors.New("device token is required")
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	encodedCapabilities, err := json.Marshal(capabilities)
	if err != nil {
		return DeviceRecord{}, fmt.Errorf("encode device capabilities: %w", err)
	}
	tokenHash := hashDeviceToken(plaintextToken)
	if _, err := s.db.Exec(`
		INSERT INTO devices
			(device_id, display_name, token_hash, capabilities, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		deviceID, displayName, tokenHash, string(encodedCapabilities), createdAt,
	); err != nil {
		return DeviceRecord{}, fmt.Errorf("register device: %w", err)
	}
	return DeviceRecord{
		DeviceID:     deviceID,
		DisplayName:  displayName,
		TokenHash:    tokenHash,
		Capabilities: append([]string(nil), capabilities...),
		CreatedAt:    createdAt,
	}, nil
}

// AuthenticateDevice verifies a credential against the principal named by
// deviceID. The fixed-length secret hash comparison is constant-time. The
// preceding database lookup is not an anti-enumeration guarantee: device IDs
// are public principals and may have observably different lookup timing.
// Revoked principals never authenticate, even when the credential is otherwise
// correct.
func (s *Store) AuthenticateDevice(deviceID, plaintextToken string) (DeviceRecord, bool, error) {
	record, err := s.deviceByID(deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		// Preserve the constant-time comparison path for unknown principals.
		zeroHash := make([]byte, sha256.Size)
		candidate, _ := hex.DecodeString(hashDeviceToken(plaintextToken))
		_ = subtle.ConstantTimeCompare(zeroHash, candidate)
		return DeviceRecord{}, false, nil
	}
	if err != nil {
		return DeviceRecord{}, false, err
	}

	stored, err := hex.DecodeString(record.TokenHash)
	if err != nil || len(stored) != sha256.Size {
		return DeviceRecord{}, false, errors.New("invalid stored device token hash")
	}
	candidate, _ := hex.DecodeString(hashDeviceToken(plaintextToken))
	matches := subtle.ConstantTimeCompare(stored, candidate) == 1
	return record, matches && record.RevokedAt == 0, nil
}

// TouchDevice records server receipt time. MAX prevents a later database write
// from moving last_seen_at backwards when requests complete out of order.
func (s *Store) TouchDevice(deviceID string, receivedAt int64) error {
	result, err := s.db.Exec(
		`UPDATE devices SET last_seen_at = MAX(last_seen_at, ?) WHERE device_id = ?`,
		receivedAt, deviceID,
	)
	if err != nil {
		return err
	}
	return requireAffectedDevice(result)
}

// UpdateDeviceMetadata changes only user-facing mutable metadata. Device
// identity, credentials, lifecycle timestamps, and revocation state remain
// untouched.
func (s *Store) UpdateDeviceMetadata(deviceID, displayName string, capabilities []string) error {
	if capabilities == nil {
		capabilities = []string{}
	}
	encodedCapabilities, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("encode device capabilities: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE devices SET display_name = ?, capabilities = ? WHERE device_id = ?`,
		displayName, string(encodedCapabilities), deviceID,
	)
	if err != nil {
		return err
	}
	return requireAffectedDevice(result)
}

// RevokeDevice disables a device credential at the supplied server timestamp.
func (s *Store) RevokeDevice(deviceID string, revokedAt int64) error {
	result, err := s.db.Exec(
		`UPDATE devices SET revoked_at = ? WHERE device_id = ?`,
		revokedAt, deviceID,
	)
	if err != nil {
		return err
	}
	return requireAffectedDevice(result)
}

func (s *Store) deviceByID(deviceID string) (DeviceRecord, error) {
	var record DeviceRecord
	var capabilitiesJSON string
	err := s.db.QueryRow(`
		SELECT device_id, display_name, token_hash, capabilities,
		       created_at, last_seen_at, revoked_at
		FROM devices
		WHERE device_id = ?`,
		deviceID,
	).Scan(
		&record.DeviceID,
		&record.DisplayName,
		&record.TokenHash,
		&capabilitiesJSON,
		&record.CreatedAt,
		&record.LastSeenAt,
		&record.RevokedAt,
	)
	if err != nil {
		return DeviceRecord{}, err
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &record.Capabilities); err != nil {
		return DeviceRecord{}, fmt.Errorf("decode device capabilities: %w", err)
	}
	return record, nil
}

func requireAffectedDevice(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func hashDeviceToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	if value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
