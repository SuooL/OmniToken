package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestNewEnforcesSecureServerURLPolicy(t *testing.T) {
	tests := []struct {
		name          string
		serverURL     string
		allowInsecure bool
		wantErr       bool
	}{
		{name: "empty", serverURL: "", wantErr: true},
		{name: "https remote", serverURL: "https://hub.example"},
		{name: "https trailing slash", serverURL: "https://hub.example/"},
		{name: "http IPv4 loopback", serverURL: "http://127.0.0.1:8787"},
		{name: "http IPv6 loopback", serverURL: "http://[::1]:8787"},
		{name: "http localhost", serverURL: "http://localhost:8787"},
		{name: "explicit insecure remote", serverURL: "http://hub.example:8787", allowInsecure: true},
		{name: "plaintext remote", serverURL: "http://hub.example:8787", wantErr: true},
		{name: "missing scheme", serverURL: "hub.example:8787", wantErr: true},
		{name: "unsupported scheme", serverURL: "ftp://hub.example", wantErr: true},
		{name: "missing host", serverURL: "https:///api", wantErr: true},
		{name: "userinfo", serverURL: "https://user:secret@hub.example", wantErr: true},
		{name: "query", serverURL: "https://hub.example?token=secret", wantErr: true},
		{name: "fragment", serverURL: "https://hub.example#fragment", wantErr: true},
		{name: "base path", serverURL: "https://hub.example/base", wantErr: true},
		{name: "encoded path", serverURL: "https://hub.example/%2f", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := New(Config{
				ServerURL:         tc.serverURL,
				AllowInsecureHTTP: tc.allowInsecure,
				StatePath:         filepath.Join(t.TempDir(), "state.json"),
			})
			if tc.wantErr {
				if err == nil {
					_ = agent.Close()
					t.Fatalf("New accepted unsafe or malformed URL %q", tc.serverURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("New rejected %q: %v", tc.serverURL, err)
			}
			if err := agent.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewRequiresRelayTokenWhenRelayEnabled(t *testing.T) {
	base := Config{
		ServerURL:   "https://hub.example",
		StatePath:   filepath.Join(t.TempDir(), "state.json"),
		RelayListen: "127.0.0.1:0",
	}
	if agent, err := New(base); err == nil {
		_ = agent.Close()
		t.Fatal("New enabled an unauthenticated relay")
	}
	base.RelayToken = "relay-secret"
	agent, err := New(base)
	if err != nil {
		t.Fatalf("New rejected authenticated relay: %v", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayRequiresIndependentCredentialAndPreservesUpstreamIdentity(t *testing.T) {
	var upstreamCalls atomic.Int32
	type receivedRequest struct {
		path          string
		authorization string
		deviceID      string
		adminAuth     string
		relayToken    string
		body          string
	}
	received := make(chan receivedRequest, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		received <- receivedRequest{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			deviceID:      r.Header.Get("X-OmniToken-Device-ID"),
			adminAuth:     r.Header.Get("X-OmniToken-Admin-Authorization"),
			relayToken:    r.Header.Get(relayTokenHeader),
			body:          string(body),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	agent := &Agent{cfg: Config{ServerURL: upstream.URL, RelayToken: "relay-secret"}}
	server, err := agent.relayServer()
	if err != nil {
		t.Fatal(err)
	}

	for _, relayCredentials := range [][]string{
		nil,
		{"wrong-secret"},
		{"relay-secret", "relay-secret"},
	} {
		unauthorized := httptest.NewRequest(http.MethodPost, "/api/v2/ingest", strings.NewReader(`{"batch":"blocked"}`))
		unauthorized.Header.Set("Authorization", "Bearer device-secret")
		for _, credential := range relayCredentials {
			unauthorized.Header.Add(relayTokenHeader, credential)
		}
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, unauthorized)
		if response.Code != http.StatusUnauthorized || upstreamCalls.Load() != 0 {
			t.Fatalf("relay credentials=%v status=%d upstream calls=%d",
				relayCredentials, response.Code, upstreamCalls.Load())
		}
	}

	for _, path := range []string{"/api/v1/ingest", "/api/v2/ingest", "/api/v2/heartbeat"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"payload":"forwarded"}`))
		request.Header.Set(relayTokenHeader, "relay-secret")
		request.Header.Set("Authorization", "Bearer device-secret")
		request.Header.Set("X-OmniToken-Device-ID", "device-id")
		request.Header.Set("X-OmniToken-Admin-Authorization", "Bearer admin-secret")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		got := <-received
		if got.path != path || got.authorization != "Bearer device-secret" ||
			got.deviceID != "device-id" || got.adminAuth != "Bearer admin-secret" ||
			got.relayToken != "relay-secret" || got.body != `{"payload":"forwarded"}` {
			t.Fatalf("forwarded request = %+v", got)
		}
	}
}

func TestRelayUsesDifferentCredentialForNextHop(t *testing.T) {
	var relayToken, authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayToken = r.Header.Get(relayTokenHeader)
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	agent := &Agent{cfg: Config{
		ServerURL:          upstream.URL,
		RelayToken:         "local-listener-secret",
		RelayUpstreamToken: "next-hop-secret",
	}}
	server, err := agent.relayServer()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/ingest", strings.NewReader(`{}`))
	request.Header.Set(relayTokenHeader, "local-listener-secret")
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if relayToken != "next-hop-secret" || authorization != "Bearer device-secret" {
		t.Fatalf("next-hop relay token=%q authorization=%q", relayToken, authorization)
	}
}

func TestRelayAllowsOnlyExplicitPostRoutesAndOpenHealth(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	agent := &Agent{cfg: Config{ServerURL: upstream.URL, RelayToken: "do-not-leak"}}
	server, err := agent.relayServer()
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	server.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK || strings.Contains(health.Body.String(), "do-not-leak") {
		t.Fatalf("health status=%d body=%q", health.Code, health.Body.String())
	}

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/api/v1/ingest", want: http.StatusMethodNotAllowed},
		{method: http.MethodPut, path: "/api/v2/ingest", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/v2/heartbeat", want: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/api/v2/enroll", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/settings", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v2/ingest/extra", want: http.StatusNotFound},
	}
	for _, tc := range tests {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set(relayTokenHeader, "do-not-leak")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Errorf("%s %s status=%d, want %d", tc.method, tc.path, response.Code, tc.want)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("disallowed routes reached upstream %d times", upstreamCalls.Load())
	}
}

func TestRelayRejectsOversizedProtocolBodiesBeforeForward(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	agent := &Agent{cfg: Config{ServerURL: upstream.URL, RelayToken: "relay-secret"}}
	server, err := agent.relayServer()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		max  int64
	}{
		{path: "/api/v1/ingest", max: relayV1IngestBodyMax},
		{path: "/api/v2/ingest", max: model.MaxIngestEnvelopeBytes},
		{path: "/api/v2/heartbeat", max: relayHeartbeatBodyMax},
	}
	for _, tc := range tests {
		request := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader([]byte("{}")))
		request.ContentLength = tc.max + 1
		request.Header.Set(relayTokenHeader, "relay-secret")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s status=%d, want 413", tc.path, response.Code)
		}
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v2/heartbeat",
		io.LimitReader(strings.NewReader(strings.Repeat("x", int(relayHeartbeatBodyMax+1))), relayHeartbeatBodyMax+1),
	)
	request.ContentLength = -1 // exercise chunked/unknown-length enforcement
	request.Header.Set(relayTokenHeader, "relay-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized heartbeat status=%d, want 413", response.Code)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("oversized bodies reached upstream %d times", upstreamCalls.Load())
	}
}

func TestRelayServerHasDefensiveTimeouts(t *testing.T) {
	agent := &Agent{cfg: Config{ServerURL: "https://hub.example", RelayToken: "relay-secret", RelayListen: ":8788"}}
	server, err := agent.relayServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v, want 10s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout=%v, want 30s", server.ReadTimeout)
	}
	if server.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout=%v, want 60s", server.WriteTimeout)
	}
	if server.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout=%v, want 2m", server.IdleTimeout)
	}
}

func TestAgentAddsRelayCredentialWithoutReplacingDeviceAuthorization(t *testing.T) {
	var relayToken, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayToken = r.Header.Get(relayTokenHeader)
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	agent := &Agent{
		cfg: Config{
			ServerURL:          server.URL,
			Token:              "device-secret",
			RelayToken:         "listener-secret",
			RelayUpstreamToken: "relay-secret",
		},
		client: server.Client(),
	}
	if err := agent.postIngest(map[string]any{"events": []model.Event{}}); err != nil {
		t.Fatal(err)
	}
	if relayToken != "relay-secret" || authorization != "Bearer device-secret" {
		t.Fatalf("relay token=%q authorization=%q", relayToken, authorization)
	}
}

func TestAgentAddsRelayCredentialToV2IngestAndHeartbeat(t *testing.T) {
	type credentials struct {
		relayToken    string
		authorization string
	}
	received := map[string]credentials{}
	agent := v2TestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		received[r.URL.Path] = credentials{
			relayToken:    r.Header.Get(relayTokenHeader),
			authorization: r.Header.Get("Authorization"),
		}
		if r.URL.Path == "/api/v2/ingest" {
			var envelope model.IngestEnvelopeV2
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			writeAck(t, w, envelope)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	agent.cfg.RelayToken = "listener-secret"
	agent.cfg.RelayUpstreamToken = "next-hop-secret"
	agent.bootID = outboxBootID
	if err := agent.outbox.Enqueue(outboxEnvelope(outboxBatchA, 1)); err != nil {
		t.Fatal(err)
	}
	if err := agent.uploadOldest(); err != nil {
		t.Fatal(err)
	}
	if err := agent.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/v2/ingest", "/api/v2/heartbeat"} {
		got := received[path]
		if got.relayToken != "next-hop-secret" || got.authorization != "Bearer device-secret" {
			t.Fatalf("%s relay token=%q authorization=%q", path, got.relayToken, got.authorization)
		}
	}
}
