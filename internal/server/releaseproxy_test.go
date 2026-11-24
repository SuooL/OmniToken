package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/agent"
	"github.com/suool/omnitoken/internal/selfupdate"
	"github.com/suool/omnitoken/internal/store"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newReleaseProxyTestServer(t *testing.T, mirrorURL string) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hub.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{
		cfg: &Config{
			Listen:           ":8787",
			DBPath:           dbPath,
			Token:            "ingest-secret",
			ReadToken:        "read-secret",
			AdminToken:       "admin-secret",
			Version:          "v0.7.1",
			ReleaseMirrorURL: mirrorURL,
		},
		store: st,
		bcast: newBroadcaster(),
		now:   func() time.Time { return time.UnixMilli(heartbeatReceivedAt) },
	}, st
}

func TestHeartbeatV2AdvertisesHubVersionAndReleaseProxy(t *testing.T) {
	s, st := newReleaseProxyTestServer(t, "")
	if _, err := st.RegisterDevice(testV2DeviceA, "Agent", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.handleHeartbeatV2(rec, jsonRequest(t, http.MethodPost, "/api/v2/heartbeat", validHeartbeat(), "device-a-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		HubVersion   string `json:"hub_version"`
		ReleaseProxy bool   `json:"release_proxy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HubVersion != "v0.7.1" || !body.ReleaseProxy {
		t.Fatalf("heartbeat response = %+v, want v0.7.1 + release_proxy=true", body)
	}

	s.cfg.ReleaseProxyDisabled = true
	rec2 := httptest.NewRecorder()
	s.handleHeartbeatV2(rec2, jsonRequest(t, http.MethodPost, "/api/v2/heartbeat", validHeartbeat(), "device-a-token"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("disabled status=%d", rec2.Code)
	}
	body.ReleaseProxy = true
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReleaseProxy {
		t.Fatalf("expected release_proxy=false when disabled, got %+v", body)
	}
}

func TestReleaseProxyAuthAndValidation(t *testing.T) {
	payload := []byte("omnitoken-linux-amd64-binary-bytes")
	checksums := fmt.Sprintf("%s  omnitoken-linux-amd64\n", sha256Hex(payload))
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v0.7.1/SHA256SUMS":
			_, _ = w.Write([]byte(checksums))
		case "/releases/download/v0.7.1/omnitoken-linux-amd64":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()

	s, st := newReleaseProxyTestServer(t, mirror.URL)
	if _, err := st.RegisterDevice(testV2DeviceA, "AgentA", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterDevice(testV2DeviceB, "AgentB", "device-b-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeDevice(testV2DeviceB, 20); err != nil {
		t.Fatal(err)
	}

	handler := s.routes()

	doReq := func(path, auth, deviceID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if deviceID != "" {
			req.Header.Set(deviceIDHeader, deviceID)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated / revoked / mismatched device rejected.
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status=%d, want 401", rec.Code)
	}
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "device-b-token", testV2DeviceB); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device status=%d, want 401", rec.Code)
	}
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "read-secret", testV2DeviceA); rec.Code != http.StatusUnauthorized {
		t.Fatalf("device header with non-device token status=%d, want 401", rec.Code)
	}
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "device-a-token", "not-a-uuid"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid device UUID status=%d, want 401", rec.Code)
	}

	// Valid v2 device and read/ingest tokens succeed.
	if rec := doReq("/api/v2/releases/v0.7.1/omnitoken-linux-amd64", "device-a-token", testV2DeviceA); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("device auth status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "read-secret", ""); rec.Code != http.StatusOK {
		t.Fatalf("read token auth status=%d", rec.Code)
	}
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "ingest-secret", ""); rec.Code != http.StatusOK {
		t.Fatalf("ingest token auth status=%d", rec.Code)
	}

	// Invalid version / disallowed asset rejected with 400.
	for _, bad := range []string{
		"/api/v2/releases/dev/SHA256SUMS",
		"/api/v2/releases/v0.7/SHA256SUMS",
		"/api/v2/releases/v0.7.1-rc1/SHA256SUMS",
		"/api/v2/releases/v0.7.1/config.json",
		"/api/v2/releases/v0.7.1/omnitoken-freebsd-amd64",
	} {
		if rec := doReq(bad, "device-a-token", testV2DeviceA); rec.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d, want 400", bad, rec.Code)
		}
	}

	// Missing upstream version returns 404.
	if rec := doReq("/api/v2/releases/v9.9.9/SHA256SUMS", "device-a-token", testV2DeviceA); rec.Code != http.StatusNotFound {
		t.Fatalf("missing version status=%d, want 404", rec.Code)
	}

	// Disabled proxy returns 404.
	s.cfg.ReleaseProxyDisabled = true
	if rec := doReq("/api/v2/releases/v0.7.1/SHA256SUMS", "device-a-token", testV2DeviceA); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled proxy status=%d, want 404", rec.Code)
	}
}

func TestReleaseProxyCachesSingleflightRejectsTamperedAndPrunes(t *testing.T) {
	goodBin := []byte("verified-release-binary-v0.7.1")
	tamperedBin := []byte("corrupted-release-binary")
	var binaryFetches, manifestFetches atomic.Int32

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v0.7.1/SHA256SUMS":
			manifestFetches.Add(1)
			fmt.Fprintf(w, "%s  omnitoken-linux-amd64\n%s  omnitoken-darwin-arm64\n",
				sha256Hex(goodBin), sha256Hex([]byte("expected-darwin-bytes")))
		case "/releases/download/v0.7.1/omnitoken-linux-amd64":
			binaryFetches.Add(1)
			time.Sleep(40 * time.Millisecond) // overlap concurrent callers
			_, _ = w.Write(goodBin)
		case "/releases/download/v0.7.1/omnitoken-darwin-arm64":
			_, _ = w.Write(tamperedBin) // checksum mismatch!
		default:
			// Serve dummy checksums for v0.6.0, v0.7.0, v0.7.2, v0.7.3 pruning checks.
			if filepath.Base(r.URL.Path) == "SHA256SUMS" {
				fmt.Fprintf(w, "%s  omnitoken-linux-amd64\n", sha256Hex(goodBin))
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()

	s, _ := newReleaseProxyTestServer(t, mirror.URL)
	handler := s.routes()

	// Concurrent requests for the same asset hit upstream only once.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/v2/releases/v0.7.1/omnitoken-linux-amd64", nil)
			req.Header.Set("Authorization", "Bearer read-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), goodBin) {
				t.Errorf("concurrent fetch status=%d len=%d", rec.Code, rec.Body.Len())
			}
		}()
	}
	wg.Wait()

	// Subsequent cached request also does not hit upstream.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/releases/v0.7.1/omnitoken-linux-amd64", nil)
	req.Header.Set("Authorization", "Bearer read-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), goodBin) {
		t.Fatalf("cached fetch status=%d", rec.Code)
	}
	if binaryFetches.Load() != 1 || manifestFetches.Load() != 1 {
		t.Fatalf("upstream fetches binary=%d manifest=%d, want 1 and 1",
			binaryFetches.Load(), manifestFetches.Load())
	}

	// Tampered binary is rejected with 502 and never cached on disk.
	badReq := httptest.NewRequest(http.MethodGet, "/api/v2/releases/v0.7.1/omnitoken-darwin-arm64", nil)
	badReq.Header.Set("Authorization", "Bearer read-secret")
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadGateway {
		t.Fatalf("tampered asset status=%d, want 502", badRec.Code)
	}
	if _, err := os.Stat(filepath.Join(s.releaseCacheRoot(), "v0.7.1", "omnitoken-darwin-arm64")); !os.IsNotExist(err) {
		t.Fatalf("tampered asset was cached on disk: err=%v", err)
	}

	// Caching >3 versions prunes the oldest semver directory.
	for _, ver := range []string{"v0.6.0", "v0.7.0", "v0.7.2"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v2/releases/"+ver+"/SHA256SUMS", nil)
		r.Header.Set("Authorization", "Bearer read-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("fetch %s status=%d", ver, w.Code)
		}
	}
	entries, err := os.ReadDir(s.releaseCacheRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != releaseCacheMaxVersions {
		t.Fatalf("cached versions count=%d, want %d", len(entries), releaseCacheMaxVersions)
	}
	if _, err := os.Stat(filepath.Join(s.releaseCacheRoot(), "v0.6.0")); !os.IsNotExist(err) {
		t.Fatalf("oldest cached version v0.6.0 was not pruned")
	}
}

// TestEndToEndReleaseProxyThroughRelay verifies the full intranet path:
// downstream agent -> relay agent -> Hub release proxy -> upstream mirror,
// including device auth, relay token swap, and SHA-256 verification.
func TestEndToEndReleaseProxyThroughRelay(t *testing.T) {
	asset, err := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	// Use our own test binary as a valid executable payload that passes
	// selfupdate's SHA-256 verification (smoke test is skipped by specifying
	// cross-target GOOS/GOARCH so we don't exec go test binary).
	crossOS, crossArch := "linux", "arm64"
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		crossOS, crossArch = "linux", "amd64"
	}
	crossAsset, err := selfupdate.AssetName(crossOS, crossArch)
	if err != nil {
		t.Fatal(err)
	}
	_ = asset
	binBytes := []byte("verified-hub-proxied-omnitoken-v0.7.1-payload")
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(binBytes), crossAsset)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v0.7.1/SHA256SUMS":
			_, _ = w.Write([]byte(checksums))
		case "/releases/download/v0.7.1/" + crossAsset:
			_, _ = w.Write(binBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()

	hubSrv, st := newReleaseProxyTestServer(t, mirror.URL)
	if _, err := st.RegisterDevice(testV2DeviceA, "IntranetNode", "intranet-device-secret", nil, 10); err != nil {
		t.Fatal(err)
	}
	hubHTTP := httptest.NewServer(hubSrv.routes())
	defer hubHTTP.Close()

	// Spin up an agent relay forwarding to hubHTTP over loopback (subnet).
	relayAgent, err := agent.New(agent.Config{
		ServerURL:         hubHTTP.URL,
		AllowInsecureHTTP: true,
		SubnetOnly:        true,
		RelayListen:       "127.0.0.1:0",
		RelayToken:        "lan-relay-secret",
		StatePath:         filepath.Join(t.TempDir(), "relay-state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer relayAgent.Close()

	relayHandler, err := relayAgent.RelayHandler()
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(relayHandler)
	defer relayHTTP.Close()

	// Downstream intranet client downloads v0.7.1 strictly through the relay.
	fc := agent.FileConfig{
		Server:             relayHTTP.URL,
		AllowInsecureHTTP:  true,
		SubnetOnly:         true,
		ProtocolVersion:    2,
		DeviceID:           testV2DeviceA,
		DeviceToken:        "intranet-device-secret",
		RelayUpstreamToken: "lan-relay-secret",
	}
	dlClient, serverURL, err := agent.NewReleaseHTTPClient(fc)
	if err != nil {
		t.Fatal(err)
	}
	targetBin := filepath.Join(t.TempDir(), "omnitoken")
	if err := os.WriteFile(targetBin, []byte("old-v0.7.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := selfupdate.Run(context.Background(), selfupdate.Options{
		CurrentVersion: "v0.7.0",
		TargetVersion:  "v0.7.1",
		BaseURL:        serverURL + "/api/v2/releases/v0.7.1",
		BinaryPath:     targetBin,
		GOOS:           crossOS,
		GOARCH:         crossArch,
		HTTPClient:     dlClient,
	})
	if err != nil {
		t.Fatalf("end-to-end relay self-update failed: %v", err)
	}
	if !res.Updated || res.TargetVersion != "v0.7.1" {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(targetBin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binBytes) {
		t.Fatalf("installed binary mismatch: got %q, want %q", got, binBytes)
	}
}
