package store

import (
	"database/sql"
	"encoding/json"
)

// App settings (F23/GAP-5): a tiny key/value side table for things the user
// edits from the settings page — pricing overrides, display currency, device
// display names. Kept out of config.json on purpose: the panel must be able to
// change them at runtime with no restart, and the DB is the single artifact
// that already travels with the server's data directory.
//
// Values are opaque TEXT; structured settings are stored as JSON via
// GetSettingsJSON/SetSettingsJSON. Settings are *display/valuation* inputs
// only — never facts. Nothing here rewrites stored events (ADR-0005: costs are
// computed at query time), so any change is reversible by editing it back.
const settingsSchema = `
CREATE TABLE IF NOT EXISTS app_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);
`

// ensureSettings creates the table on demand so settings work even on a
// database opened before this table existed (Open() also runs settingsSchema
// once the caller wires it in; both paths are idempotent). Settings reads are
// rare (page load / save), so the extra DDL round-trip is free.
func (s *Store) ensureSettings() error {
	_, err := s.db.Exec(settingsSchema)
	return err
}

// GetSetting returns the stored value, or "" when the key was never set.
// A missing key is not an error: callers apply their own default.
func (s *Store) GetSetting(key string) (string, error) {
	if err := s.ensureSettings(); err != nil {
		return "", err
	}
	var v string
	err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetSetting upserts a value.
func (s *Store) SetSetting(key, value string) error {
	if err := s.ensureSettings(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetSettingsJSON decodes the stored JSON document into out. A key that was
// never set leaves out untouched and returns nil — the caller's zero value or
// pre-filled default stands. Corrupt JSON *is* an error (no silent fallback:
// surface it rather than quietly reverting the user's settings).
func (s *Store) GetSettingsJSON(key string, out any) error {
	raw, err := s.GetSetting(key)
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

// SetSettingsJSON stores v as a JSON document under key.
func (s *Store) SetSettingsJSON(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(key, string(data))
}
