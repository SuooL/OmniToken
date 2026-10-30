package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const machineIdentitySchema = `CREATE TABLE IF NOT EXISTS device_machine_bindings (
 machine_id TEXT PRIMARY KEY,
 device_id TEXT NOT NULL UNIQUE REFERENCES devices(device_id)
);`

// EnrollMachineDevice is called only after admin authentication. The machine
// key is a lookup key, not an authentication factor. Reinstallation rotates
// credentials on the existing principal without touching history or receipts.
func (s *Store) EnrollMachineDevice(machineID, requestedID, name, token string, capabilities []string, createdAt int64) (DeviceRecord, error) {
	if !isCanonicalUUID(machineID) || !isCanonicalUUID(requestedID) || token == "" || name == "" {
		return DeviceRecord{}, errors.New("invalid machine enrollment")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return DeviceRecord{}, err
	}
	defer tx.Rollback()
	canonical := requestedID
	var bound string
	err = tx.QueryRow(`SELECT device_id FROM device_machine_bindings WHERE machine_id=?`, machineID).Scan(&bound)
	if err == nil {
		canonical = bound
		// A copied config pointing at a different existing principal is a conflict,
		// not a reason to overwrite that principal's credentials or attribution.
		if requestedID != canonical && requestedID != machineID {
			return DeviceRecord{}, errors.New("machine identity conflict")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DeviceRecord{}, err
	}
	var record DeviceRecord
	var caps string
	err = tx.QueryRow(`SELECT device_id,display_name,token_hash,capabilities,created_at,last_seen_at,revoked_at FROM devices WHERE device_id=?`, canonical).Scan(&record.DeviceID, &record.DisplayName, &record.TokenHash, &caps, &record.CreatedAt, &record.LastSeenAt, &record.RevokedAt)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeviceRecord{}, err
	}
	if exists && record.RevokedAt != 0 {
		return DeviceRecord{}, errors.New("device revoked")
	}
	// First-time binding of a legacy ID requires its current credential, even
	// with admin authorization, so a copied config cannot claim another device.
	if exists && bound == "" && record.TokenHash != hashDeviceToken(token) {
		return DeviceRecord{}, errors.New("legacy identity credential conflict")
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return DeviceRecord{}, err
	}
	if exists {
		_, err = tx.Exec(`UPDATE devices SET display_name=?,token_hash=?,capabilities=? WHERE device_id=?`, name, hashDeviceToken(token), string(encoded), canonical)
	} else {
		_, err = tx.Exec(`INSERT INTO devices(device_id,display_name,token_hash,capabilities,created_at) VALUES(?,?,?,?,?)`, canonical, name, hashDeviceToken(token), string(encoded), createdAt)
		record.DeviceID, record.CreatedAt = canonical, createdAt
	}
	if err != nil {
		return DeviceRecord{}, err
	}
	_, err = tx.Exec(`INSERT INTO device_machine_bindings(machine_id,device_id) VALUES(?,?) ON CONFLICT(machine_id) DO NOTHING`, machineID, canonical)
	if err != nil {
		return DeviceRecord{}, fmt.Errorf("bind machine identity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DeviceRecord{}, err
	}
	record.DisplayName, record.TokenHash, record.Capabilities = name, hashDeviceToken(token), capabilities
	return record, nil
}
