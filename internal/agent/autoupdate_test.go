package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/selfupdate"
)

func TestSubnetOnlyServerURLPolicy(t *testing.T) {
	tests := []struct {
		name             string
		serverURL        string
		resolveIP        string
		autoUpdate       bool
		autoUpdateSource string
		wantErr          bool
	}{
		{name: "loopback ipv4", serverURL: "http://127.0.0.1:8787"},
		{name: "loopback ipv6", serverURL: "http://[::1]:8787"},
		{name: "localhost", serverURL: "http://localhost:8787"},
		{name: "rfc1918 192.168", serverURL: "http://192.168.10.124:8787"},
		{name: "rfc1918 10", serverURL: "http://10.30.4.54:8787"},
		{name: "rfc1918 172.16", serverURL: "http://172.16.5.10:8787"},
		{name: "cgnat overlay 100.64", serverURL: "http://100.64.1.2:8787"},
		{name: "cgnat overlay 100.127", serverURL: "http://100.127.255.1:8787"},
		{name: "link local", serverURL: "http://169.254.1.2:8787"},
		{name: "ipv6 ula", serverURL: "http://[fd00::1]:8787"},
		{name: "public domain rejected", serverURL: "https://ingest.suool.net", wantErr: true},
		{name: "public ip rejected", serverURL: "http://8.163.138.198:8787", wantErr: true},
		{name: "non-cgnat 100.128 rejected", serverURL: "http://100.128.0.1:8787", wantErr: true},
		{name: "public resolve_ip rejected", serverURL: "http://192.168.10.124:8787", resolveIP: "8.163.138.198", wantErr: true},
		{name: "github auto_update rejected under subnet_only", serverURL: "http://192.168.10.124:8787", autoUpdate: true, autoUpdateSource: AutoUpdateSourceGitHub, wantErr: true},
		{name: "hub auto_update allowed under subnet_only", serverURL: "http://192.168.10.124:8787", autoUpdate: true, autoUpdateSource: AutoUpdateSourceHub},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := New(Config{
				ServerURL:         tc.serverURL,
				ResolveIP:         tc.resolveIP,
				AllowInsecureHTTP: true,
				SubnetOnly:        true,
				AutoUpdate:        tc.autoUpdate,
				AutoUpdateSource:  tc.autoUpdateSource,
				StatePath:         filepath.Join(t.TempDir(), "state.json"),
			})
			if tc.wantErr {
				if err == nil {
					_ = a.Close()
					t.Fatalf("New accepted disallowed subnet_only config: %+v", tc)
				}
				return
			}
			if err != nil {
				t.Fatalf("New rejected valid subnet_only config: %v", err)
			}
			_ = a.Close()
		})
	}
}

func TestRelayForwardsReleaseProxyGETWithCredentials(t *testing.T) {
	var upstreamCalls atomic.Int32
	var gotAuth, gotDeviceID, gotRelay, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		gotAuth = r.Header.Get("Authorization")
		gotDeviceID = r.Header.Get(deviceIDHeader)
		gotRelay = r.Header.Get(relayTokenHeader)
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("release-bytes"))
	}))
	defer upstream.Close()

	a := &Agent{cfg: Config{
		ServerURL:          upstream.URL,
		RelayToken:         "listener-secret",
		RelayUpstreamToken: "upstream-secret",
	}}
	srv, err := a.relayServer()
	if err != nil {
		t.Fatal(err)
	}

	// Missing relay token rejected.
	unauth := httptest.NewRequest(http.MethodGet, "/api/v2/releases/v0.7.1/SHA256SUMS", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized || upstreamCalls.Load() != 0 {
		t.Fatalf("unauth status=%d upstream=%d", rec.Code, upstreamCalls.Load())
	}

	// POST to release route rejected (405).
	postReq := httptest.NewRequest(http.MethodPost, "/api/v2/releases/v0.7.1/SHA256SUMS", nil)
	postReq.Header.Set(relayTokenHeader, "listener-secret")
	recPost := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recPost, postReq)
	if recPost.Code != http.StatusMethodNotAllowed || upstreamCalls.Load() != 0 {
		t.Fatalf("POST release status=%d upstream=%d", recPost.Code, upstreamCalls.Load())
	}

	// Valid GET forwarded with device Authorization, Device-ID, and swapped relay token.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/releases/v0.7.1/omnitoken-linux-amd64", nil)
	req.Header.Set(relayTokenHeader, "listener-secret")
	req.Header.Set("Authorization", "Bearer device-secret")
	req.Header.Set(deviceIDHeader, outboxDeviceID)
	recOK := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recOK, req)
	if recOK.Code != http.StatusOK || recOK.Body.String() != "release-bytes" {
		t.Fatalf("forwarded status=%d body=%q", recOK.Code, recOK.Body.String())
	}
	if gotPath != "/api/v2/releases/v0.7.1/omnitoken-linux-amd64" ||
		gotAuth != "Bearer device-secret" ||
		gotDeviceID != outboxDeviceID ||
		gotRelay != "upstream-secret" {
		t.Fatalf("forwarded headers mismatch: path=%q auth=%q dev=%q relay=%q",
			gotPath, gotAuth, gotDeviceID, gotRelay)
	}
}

func TestHeartbeatAutoUpdateTriggersHubDownloadAndReexec(t *testing.T) {
	var gotHB model.Heartbeat
	var gotRelAuth, gotRelDeviceID, gotRelRelay string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/heartbeat":
			_ = json.NewDecoder(r.Body).Decode(&gotHB)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"protocol_version":2,"hub_version":"v0.7.1","release_proxy":true}`))
		case "/api/v2/releases/v0.7.1/SHA256SUMS":
			gotRelAuth = r.Header.Get("Authorization")
			gotRelDeviceID = r.Header.Get(deviceIDHeader)
			gotRelRelay = r.Header.Get(relayTokenHeader)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()

	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	var updateOpts selfupdate.Options
	var reexecPath string
	a := &Agent{
		cfg: Config{
			ServerURL:          hub.URL,
			ProtocolVersion:    model.IngestProtocolV2,
			DeviceID:           outboxDeviceID,
			DeviceToken:        "device-secret",
			RelayUpstreamToken: "relay-secret",
			AgentVersion:       "v0.7.0",
			AutoUpdate:         true,
			AutoUpdateSource:   AutoUpdateSourceHub,
		},
		client:            hub.Client(),
		outbox:            outbox,
		bootID:            outboxBootID,
		updateSync:        true,
		updateJitterDelay: func() time.Duration { return 0 },
		procs: func(device string, now time.Time) (model.ProcReport, error) {
			return model.ProcReport{Device: device, ObservedAt: now.UnixMilli()}, nil
		},
		runSelfUpdate: func(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
			updateOpts = opts
			// Exercise the injected HTTPClient against the Hub release route.
			resp, err := opts.HTTPClient.Get(opts.BaseURL + "/SHA256SUMS")
			if err != nil {
				return selfupdate.Result{}, err
			}
			resp.Body.Close()
			return selfupdate.Result{
				CurrentVersion: opts.CurrentVersion,
				TargetVersion:  opts.TargetVersion,
				BinaryPath:     "/usr/local/bin/omnitoken",
				Updated:        true,
			}, nil
		},
		reexecSelf: func(path string) error {
			reexecPath = path
			return nil
		},
	}

	if err := a.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if updateOpts.TargetVersion != "v0.7.1" || updateOpts.BaseURL != hub.URL+"/api/v2/releases/v0.7.1" {
		t.Fatalf("unexpected selfupdate options: %+v", updateOpts)
	}
	if gotRelAuth != "Bearer device-secret" || gotRelDeviceID != outboxDeviceID || gotRelRelay != "relay-secret" {
		t.Fatalf("release auth headers: auth=%q dev=%q relay=%q", gotRelAuth, gotRelDeviceID, gotRelRelay)
	}
	if reexecPath != "/usr/local/bin/omnitoken" {
		t.Fatalf("reexecSelf path=%q, want /usr/local/bin/omnitoken", reexecPath)
	}
	hasCap := false
	for _, c := range gotHB.Capabilities {
		if c == "auto_update" {
			hasCap = true
		}
	}
	if !hasCap {
		t.Fatalf("expected auto_update capability in heartbeat, got %v", gotHB.Capabilities)
	}
}

func TestHeartbeatAutoUpdateSkipsWhenDisabledUpToDateOrNoProxy(t *testing.T) {
	tests := []struct {
		name         string
		autoUpdate   bool
		agentVersion string
		hubVersion   string
		releaseProxy bool
	}{
		{name: "disabled", autoUpdate: false, agentVersion: "v0.7.0", hubVersion: "v0.7.1", releaseProxy: true},
		{name: "dev build", autoUpdate: true, agentVersion: "dev", hubVersion: "v0.7.1", releaseProxy: true},
		{name: "same version", autoUpdate: true, agentVersion: "v0.7.1", hubVersion: "v0.7.1", releaseProxy: true},
		{name: "agent ahead", autoUpdate: true, agentVersion: "v0.8.0", hubVersion: "v0.7.1", releaseProxy: true},
		{name: "hub dev", autoUpdate: true, agentVersion: "v0.7.0", hubVersion: "dev", releaseProxy: true},
		{name: "proxy disabled on hub", autoUpdate: true, agentVersion: "v0.7.0", hubVersion: "v0.7.1", releaseProxy: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			a := &Agent{
				cfg: Config{
					ServerURL:        "http://127.0.0.1:8787",
					ProtocolVersion:  model.IngestProtocolV2,
					AgentVersion:     tc.agentVersion,
					AutoUpdate:       tc.autoUpdate,
					AutoUpdateSource: AutoUpdateSourceHub,
				},
				updateSync:        true,
				updateJitterDelay: func() time.Duration { return 0 },
				runSelfUpdate: func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
					called = true
					return selfupdate.Result{}, nil
				},
			}
			a.maybeAutoUpdate(tc.hubVersion, tc.releaseProxy)
			if called {
				t.Fatalf("auto-update unexpectedly triggered for %+v", tc)
			}
		})
	}
}

func TestHeartbeatAutoUpdateFailureReportsWarningAndBackoff(t *testing.T) {
	var warningsReceived []string
	hubVersion := "v0.7.1"
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hb model.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		warningsReceived = hb.Warnings
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocol_version":2,"hub_version":"` + hubVersion + `","release_proxy":true}`))
	}))
	defer hub.Close()

	now := time.Unix(1_785_400_000, 0)
	attempts := 0
	outbox := openTestOutbox(t, filepath.Join(t.TempDir(), "outbox.db"), DefaultOutboxMaxBytes)
	a := &Agent{
		cfg: Config{
			ServerURL:       hub.URL,
			ProtocolVersion: model.IngestProtocolV2,
			DeviceID:        outboxDeviceID,
			DeviceToken:     "device-secret",
			AgentVersion:    "v0.7.0",
			AutoUpdate:      true,
		},
		client:            hub.Client(),
		outbox:            outbox,
		bootID:            outboxBootID,
		now:               func() time.Time { return now },
		updateSync:        true,
		updateJitterDelay: func() time.Duration { return 0 },
		procs: func(device string, ts time.Time) (model.ProcReport, error) {
			return model.ProcReport{Device: device, ObservedAt: ts.UnixMilli()}, nil
		},
		runSelfUpdate: func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
			attempts++
			return selfupdate.Result{}, errors.New("permission denied")
		},
	}

	// First heartbeat attempts update and fails.
	if err := a.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1", attempts)
	}

	// Second heartbeat 1 minute later: sends warning and stays in 5m backoff.
	now = now.Add(time.Minute)
	if err := a.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected backoff to suppress retry, attempts=%d", attempts)
	}
	if len(warningsReceived) != 1 || !strings.Contains(warningsReceived[0], "auto_update v0.7.1: permission denied") {
		t.Fatalf("warnings=%v", warningsReceived)
	}

	// After 5m backoff expires, retries once.
	now = now.Add(5 * time.Minute)
	if err := a.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("expected retry after backoff, attempts=%d", attempts)
	}

	// When Hub advances to a newer version (v0.7.2), retries immediately despite v0.7.1 backoff.
	hubVersion = "v0.7.2"
	now = now.Add(time.Minute)
	if err := a.sendHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("expected immediate retry for new target v0.7.2, attempts=%d", attempts)
	}
}

func TestCleanupStaleOldBinariesInDir(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"omnitoken.exe",
		"omnitoken.exe.old.1234",
		"omnitoken.exe.old.99999",
		"omnitoken.exe.old.bak",
		"other.old.1234",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cleanupStaleOldBinariesInDir(dir, "omnitoken.exe")

	for _, removed := range []string{"omnitoken.exe.old.1234", "omnitoken.exe.old.99999"} {
		if _, err := os.Stat(filepath.Join(dir, removed)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", removed)
		}
	}
	for _, kept := range []string{"omnitoken.exe", "omnitoken.exe.old.bak", "other.old.1234"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("expected %s to remain: %v", kept, err)
		}
	}
}
