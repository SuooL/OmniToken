package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// deviceID. The hash comparison is constant-time. Revoked principals never
// authenticate, even when the credential is otherwise correct.
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
