package siteauth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testOrigin = "https://omni.example.test"

func testAuth(t *testing.T) *Auth {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "auth.db"), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func call(a *Auth, method, path, origin, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	r := httptest.NewRequest(method, testOrigin+path, strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestCodeSingleUseAndSessionRevocation(t *testing.T) {
	a := testAuth(t)
	code, err := a.GenerateCode(15 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var codeHash string
	if err := a.db.QueryRow("SELECT code_hash FROM login_codes").Scan(&codeHash); err != nil || codeHash == normalizeCode(code) || len(codeHash) != 64 {
		t.Fatal("login code must be stored only as a digest")
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	w := call(a, "POST", "/auth/code", testOrigin, string(body))
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies: %v", cs)
	}
	c := cs[0]
	if c.Name != "__Host-omnitoken" || !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", c)
	}
	r := httptest.NewRequest("GET", testOrigin+"/", nil)
	r.AddCookie(c)
	if !a.Authenticated(r) {
		t.Fatal("session missing")
	}
	var stored string
	if err := a.db.QueryRow("SELECT token_hash FROM sessions LIMIT 1").Scan(&stored); err != nil || stored == c.Value || len(stored) != 64 {
		t.Fatal("session must be stored only as a digest")
	}
	if got := call(a, "POST", "/auth/code", testOrigin, string(body)); got.Code != 401 {
		t.Fatalf("code replay: %d", got.Code)
	}
	if got := call(a, "POST", "/auth/logout", "https://evil.test", "", c); got.Code != 403 {
		t.Fatalf("CSRF logout: %d", got.Code)
	}
	if !a.Authenticated(r) {
		t.Fatal("CSRF revoked session")
	}
	if got := call(a, "POST", "/auth/logout", testOrigin, "", c); got.Code != 200 {
		t.Fatalf("logout: %d", got.Code)
	}
	if a.Authenticated(r) {
		t.Fatal("revoked session accepted")
	}
}

func TestChallengesAreBoundToPurposeBrowserAndExpiry(t *testing.T) {
	a := testAuth(t)
	w := call(a, "POST", "/auth/passkey/login/begin", testOrigin, "{}")
	if w.Code != 200 {
		t.Fatalf("begin login: %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	if _, err := a.takeChallenge(c.Value, "register", ""); err == nil {
		t.Fatal("challenge crossed purpose")
	}
	if _, err := a.takeChallenge(c.Value, "login", "different-browser"); err == nil {
		t.Fatal("challenge crossed browser binding")
	}
	if _, err := a.takeChallenge(c.Value, "login", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.takeChallenge(c.Value, "login", ""); err == nil {
		t.Fatal("challenge replay accepted")
	}
	w = call(a, "POST", "/auth/passkey/login/begin", testOrigin, "{}")
	c = w.Result().Cookies()[0]
	a.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if _, err := a.takeChallenge(c.Value, "login", ""); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestAuthDatabasePermissionsAndOriginValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	for _, origin := range []string{"http://example.test", "https://example.test/path", "https://user:pass@example.test", "https://example.test?query=1", ""} {
		if a, err := Open(path, origin); err == nil {
			a.Close()
			t.Fatalf("unsafe origin: %q", origin)
		}
	}
	a, err := Open(path, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("exposed auth database: %s %o", p, info.Mode().Perm())
		}
	}
}

func TestCodeExpiryRotationAndConcurrentConsumption(t *testing.T) {
	a := testAuth(t)
	old, _ := a.GenerateCode(time.Minute)
	fresh, _ := a.GenerateCode(time.Minute)
	if _, err := a.consumeCode(old); err == nil {
		t.Fatal("replaced code accepted")
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := a.consumeCode(fresh); results <- err == nil }()
	}
	wg.Wait()
	close(results)
	n := 0
	for ok := range results {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("successful consumptions: %d", n)
	}
	code, _ := a.GenerateCode(time.Minute)
	a.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := a.consumeCode(code); err == nil {
		t.Fatal("expired code accepted")
	}
}

func TestOriginBodyAndChallengeBoundaries(t *testing.T) {
	a := testAuth(t)
	for _, origin := range []string{"", "null", "https://evil.test", "https://omni.example.test.evil.test"} {
		if got := call(a, "POST", "/auth/passkey/login/begin", origin, "{}"); got.Code != 403 {
			t.Errorf("origin %q: %d", origin, got.Code)
		}
	}
	if got := call(a, "POST", "/auth/passkey/register/begin", testOrigin, "{}"); got.Code != 401 {
		t.Fatalf("unauthenticated registration: %d", got.Code)
	}
	if got := call(a, "POST", "/auth/passkey/login/finish", testOrigin, "{}"); got.Code != 400 {
		t.Fatalf("missing challenge: %d", got.Code)
	}
	if got := call(a, "POST", "/auth/code", testOrigin, strings.Repeat("a", 70*1024)); got.Code != 400 {
		t.Fatalf("oversized body: %d", got.Code)
	}
}

func TestAuthPersistsAndProtectsShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	a, err := Open(path, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := a.GenerateCode(time.Minute)
	token, err := a.consumeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = Open(path, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r := httptest.NewRequest("GET", testOrigin+"/", nil)
	w := httptest.NewRecorder()
	h := a.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private shell")) }))
	h.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("unauthorized shell: %d", w.Code)
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("private shell")) {
		t.Fatalf("authenticated shell: %d", w.Code)
	}
	a.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if a.Authenticated(r) {
		t.Fatal("expired session accepted")
	}
	a.Close()
	if _, err = Open(path, "https://another.example.test"); err == nil {
		t.Fatal("RP identity changed silently")
	}
}
