// Package siteauth provides single-owner browser authentication independently
// of the device protocol and its bearer credentials.
package siteauth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	_ "modernc.org/sqlite"
)

const sessionCookie = "__Host-omnitoken"
const challengeCookie = "__Host-omnitoken-challenge"
const sessionLifetime = 30 * 24 * time.Hour
const challengeLifetime = 5 * time.Minute

var errDenied = errors.New("authentication denied")

type Auth struct {
	db       *sql.DB
	origin   string
	wa       *webauthn.WebAuthn
	now      func() time.Time
	mu       sync.Mutex
	attempts map[string]attempt
	// Serialize credential validation and counter persistence across ceremonies.
	ceremonyMu sync.Mutex
}
type attempt struct {
	start time.Time
	count int
}

// Open binds a separate authentication database to one immutable RP origin.
// Changing the origin requires an explicit new database and fresh enrollment.
func Open(path, origin string) (*Auth, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("site_auth_origin must be an HTTPS origin without path, query or credentials")
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "OmniToken", RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{AuthenticatorAttachment: protocol.Platform, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Auth, error) { db.Close(); return nil, e }
	if _, err = db.Exec(`
 CREATE TABLE IF NOT EXISTS identity (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS login_codes (code_hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL, auth_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS credentials (id TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS challenges (token_hash TEXT PRIMARY KEY, kind TEXT NOT NULL, session_hash TEXT NOT NULL, data TEXT NOT NULL, expires_at INTEGER NOT NULL);
 `); err != nil {
		return fail(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return fail(err)
	}
	id, err := randomToken()
	if err != nil {
		return fail(err)
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO identity(key,value) VALUES ('origin',?),('user_id',?)", origin, id); err != nil {
		return fail(err)
	}
	var bound string
	if err = db.QueryRow("SELECT value FROM identity WHERE key='origin'").Scan(&bound); err != nil {
		return fail(err)
	}
	if bound != origin {
		return fail(errors.New("site authentication database belongs to another origin"))
	}
	return &Auth{db: db, wa: wa, origin: origin, now: time.Now, attempts: make(map[string]attempt)}, nil
}

func (a *Auth) Close() error { return a.db.Close() }
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func normalizeCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// GenerateCode replaces any outstanding code. Only its digest is persisted.
func (a *Auth) GenerateCode(ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > time.Hour {
		return "", errors.New("code lifetime must be between zero and one hour")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	tx, err := a.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM login_codes"); err != nil {
		return "", err
	}
	if _, err = tx.Exec("INSERT INTO login_codes VALUES (?,?)", digest(code), a.now().Add(ttl).Unix()); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	var groups []string
	for len(code) > 0 {
		n := min(4, len(code))
		groups = append(groups, code[:n])
		code = code[n:]
	}
	return strings.Join(groups, "-"), nil
}

func (a *Auth) consumeCode(code string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM login_codes WHERE code_hash=? AND expires_at>?", digest(normalizeCode(code)), a.now().Unix())
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", errDenied
	}
	if _, err = tx.Exec("INSERT INTO sessions VALUES (?,?,?)", digest(token), a.now().Add(sessionLifetime).Unix(), a.now().Unix()); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (a *Auth) newSession() (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = a.db.Exec("INSERT INTO sessions VALUES (?,?,?)", digest(token), a.now().Add(sessionLifetime).Unix(), a.now().Unix())
	return token, err
}

type user struct {
	id          []byte
	credentials []webauthn.Credential
}

func (u user) WebAuthnID() []byte                         { return u.id }
func (u user) WebAuthnName() string                       { return "SuooL" }
func (u user) WebAuthnDisplayName() string                { return "SuooL" }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.credentials }
func (a *Auth) loadUser() (user, error) {
	var u user
	var id string
	if err := a.db.QueryRow("SELECT value FROM identity WHERE key='user_id'").Scan(&id); err != nil {
		return u, err
	}
	var err error
	u.id, err = base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return u, err
	}
	rows, err := a.db.Query("SELECT data FROM credentials ORDER BY id")
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var c webauthn.Credential
		if err = rows.Scan(&raw); err != nil {
			return u, err
		}
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, c)
	}
	return u, rows.Err()
}

func (a *Auth) saveCredential(c *webauthn.Credential, insert bool) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(c.ID)
	if insert {
		_, err = a.db.Exec("INSERT INTO credentials VALUES (?,?,?)", id, string(raw), a.now().Unix())
		return err
	}
	result, err := a.db.Exec("UPDATE credentials SET data=?,updated_at=? WHERE id=?", string(raw), a.now().Unix(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errDenied
	}
	return err
}

func (a *Auth) storeChallenge(kind, binding string, data *webauthn.SessionData) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err = a.db.Exec("DELETE FROM challenges WHERE expires_at<=?", a.now().Unix()); err != nil {
		return "", err
	}
	_, err = a.db.Exec("INSERT INTO challenges VALUES (?,?,?,?,?)", digest(token), kind, binding, string(raw), a.now().Add(challengeLifetime).Unix())
	return token, err
}

func (a *Auth) takeChallenge(token, kind, binding string) (webauthn.SessionData, error) {
	var data webauthn.SessionData
	var raw string
	err := a.db.QueryRow("DELETE FROM challenges WHERE token_hash=? AND kind=? AND session_hash=? AND expires_at>? RETURNING data", digest(token), kind, binding, a.now().Unix()).Scan(&raw)
	if err != nil {
		return data, fmt.Errorf("challenge unavailable: %w", err)
	}
	err = json.Unmarshal([]byte(raw), &data)
	return data, err
}

func (a *Auth) allowAttempt(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for k, v := range a.attempts {
		if now.Sub(v.start) >= time.Minute {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[key]
	if v.start.IsZero() {
		if len(a.attempts) >= 4096 {
			return false
		}
		v.start = now
	}
	v.count++
	a.attempts[key] = v
	return v.count <= 20
}
