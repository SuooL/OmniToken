package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/siteauth"
)

func TestSiteLogoutStopsOpenStream(t *testing.T) {
	const origin = "https://omni.example.test"
	a, err := siteauth.Open(filepath.Join(t.TempDir(), "auth.db"), origin)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := newLiveTestServer(t)
	s.siteAuth = a
	s.streamRefreshInterval = 10 * time.Millisecond
	code, _ := a.GenerateCode(time.Minute)
	body, _ := json.Marshal(map[string]string{"code": code})
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	r := httptest.NewRequest("POST", origin+"/auth/code", strings.NewReader(string(body)))
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	httpServer := httptest.NewServer(s.routes())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/stream", nil)
	req.AddCookie(cookie)
	resp, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	r = httptest.NewRequest("POST", origin+"/auth/logout", nil)
	r.Header.Set("Origin", origin)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("revoked stream did not terminate: %v", err)
	}
	if strings.Contains(string(data), "data:") {
		t.Fatal("revoked stream delivered another payload")
	}
}

func TestSiteSessionReadsWithoutGrantingAdmin(t *testing.T) {
	const origin = "https://omni.example.test"
	a, err := siteauth.Open(filepath.Join(t.TempDir(), "auth.db"), origin)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := scopedSrv("127.0.0.1:8787", "ingest", "read", "admin")
	s.siteAuth = a
	hit := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
	r := httptest.NewRequest("GET", origin+"/api/v1/live", nil)
	w := httptest.NewRecorder()
	s.readAuth(hit)(w, r)
	if w.Code != 401 {
		t.Fatalf("site auth must override loopback exemption: %d", w.Code)
	}
	code, _ := a.GenerateCode(time.Minute)
	body, _ := json.Marshal(map[string]string{"code": code})
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	r = httptest.NewRequest("POST", origin+"/auth/code", strings.NewReader(string(body)))
	r.Header.Set("Origin", origin)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	session := w.Result().Cookies()[0]
	r = httptest.NewRequest("GET", origin+"/api/v1/live", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	s.readAuth(hit)(w, r)
	if w.Code != 204 {
		t.Fatalf("session read: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.adminAuth(hit)(w, r)
	if w.Code != 401 {
		t.Fatalf("session granted admin: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.auth(hit)(w, r)
	if w.Code != 401 {
		t.Fatalf("session granted ingest: %d", w.Code)
	}
	r = httptest.NewRequest("GET", origin+"/api/v1/live", nil)
	r.Header.Set("Authorization", "Bearer read")
	w = httptest.NewRecorder()
	s.readAuth(hit)(w, r)
	if w.Code != 204 {
		t.Fatalf("desktop bearer regression: %d", w.Code)
	}
	r = httptest.NewRequest("GET", origin+"/api/v1/live?access_token=read", nil)
	w = httptest.NewRecorder()
	s.readAuth(hit)(w, r)
	if w.Code != 401 {
		t.Fatalf("query token escaped stream: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.readAuthStream(hit)(w, r)
	if w.Code != 204 {
		t.Fatalf("legacy stream credential regression: %d", w.Code)
	}
}
