package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
)

// The rule these tests pin down (ADR-0016): whether a read needs a credential is
// derived from the listen address, never from a flag someone has to remember.
func srv(listen, token string) *Server {
	return &Server{cfg: &Config{
		Listen:     listen,
		Token:      token,
		ReadToken:  token,
		AdminToken: token,
	}}
}

func scopedSrv(listen, ingestToken, readToken, adminToken string) *Server {
	return &Server{cfg: &Config{
		Listen:     listen,
		Token:      ingestToken,
		ReadToken:  readToken,
		AdminToken: adminToken,
	}}
}

func TestLoopbackOnly(t *testing.T) {
	cases := []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:8787", true},
		{"127.0.0.53:8787", true},
		{"localhost:8787", true},
		{"[::1]:8787", true},
		// An empty host means every interface. Reading this as loopback would
		// serve the panel to the network, so it gets its own case.
		{":8787", false},
		{"0.0.0.0:8787", false},
		{"[::]:8787", false},
		{"192.168.1.10:8787", false},
		{"10.0.0.5:8787", false},
		// Unparseable must fall to the safe side, not the convenient one.
		{"garbage", false},
		{"", false},
	}
	for _, c := range cases {
		if got := srv(c.listen, "").loopbackOnly(); got != c.want {
			t.Errorf("loopbackOnly(%q) = %v, want %v", c.listen, got, c.want)
		}
	}
}

// A server that would publish usage data to the network with no credential must
// not start. A warning is not enough — the old default was exactly this shape.
func TestRefusesToServeReadableWithoutToken(t *testing.T) {
	if err := srv("0.0.0.0:8787", "").requireAuthConsistency(); err == nil {
		t.Fatal("reachable + no token must be refused at startup")
	}
	if err := srv(":8787", "").requireAuthConsistency(); err == nil {
		t.Fatal("bare :port is every interface and must be refused too")
	}
	if err := srv("192.168.1.10:8787", "s3cret").requireAuthConsistency(); err != nil {
		t.Fatalf("reachable + token is a valid setup: %v", err)
	}
	// The single-machine case must keep working with no configuration at all.
	if err := srv("127.0.0.1:8787", "").requireAuthConsistency(); err != nil {
		t.Fatalf("loopback without a token is the common case: %v", err)
	}
}

func TestReadAuthOpenOnLoopback(t *testing.T) {
	// Even with a token set: a loopback-only server keeps its reads open, or
	// every existing single-machine panel would break on upgrade.
	for _, token := range []string{"", "s3cret"} {
		h := srv("127.0.0.1:8787", token).readAuth(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/api/v1/live", nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("token=%q: got %d, want the handler to run", token, rec.Code)
		}
	}
}

func TestReadAuthRequiresTokenWhenReachable(t *testing.T) {
	h := srv("192.168.1.10:8787", "s3cret").readAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	t.Run("no header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/api/v1/live", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", rec.Code)
		}
		// So a browser navigating straight at it is told how to proceed.
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Error("401 should carry WWW-Authenticate")
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/live", nil)
		r.Header.Set("Authorization", "Bearer nope")
		h(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", rec.Code)
		}
	})

	t.Run("right token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/live", nil)
		r.Header.Set("Authorization", "Bearer s3cret")
		h(rec, r)
		if rec.Code != http.StatusTeapot {
			t.Errorf("got %d, want the handler to run", rec.Code)
		}
	})
}

// The query-parameter credential exists for EventSource, which cannot set a
// header. It must work on the stream and nowhere else — otherwise the weaker
// channel becomes a way into the other thirteen endpoints.
func TestQueryTokenAcceptedOnStreamOnly(t *testing.T) {
	s := srv("192.168.1.10:8787", "s3cret")
	run := func(h http.HandlerFunc) int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/api/v1/x?access_token=s3cret", nil))
		return rec.Code
	}
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }

	if got := run(s.readAuthStream(ok)); got != http.StatusTeapot {
		t.Errorf("stream with ?access_token: got %d, want the handler to run", got)
	}
	if got := run(s.readAuth(ok)); got != http.StatusUnauthorized {
		t.Errorf("ordinary read with ?access_token: got %d, want 401", got)
	}
	// And a wrong one is still rejected on the stream.
	rec := httptest.NewRecorder()
	s.readAuthStream(ok)(rec, httptest.NewRequest("GET", "/api/v1/x?access_token=nope", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("stream with wrong ?access_token: got %d, want 401", rec.Code)
	}
}

// The default must be the safe one: doing nothing should not publish anything.
func TestDefaultListenIsLoopback(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !(&Server{cfg: cfg}).loopbackOnly() {
		t.Errorf("default Listen = %q, want a loopback address", cfg.Listen)
	}
	if err := (&Server{cfg: cfg}).requireAuthConsistency(); err != nil {
		t.Errorf("default config must start with no token: %v", err)
	}
}

func TestLoadConfigFallsBackToLegacyTokenOnlyWhenScopedFieldsAreMissing(t *testing.T) {
	writeConfig := func(t *testing.T, body string) *Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	legacy := writeConfig(t, `{"token":"legacy"}`)
	if legacy.ReadToken != "legacy" || legacy.AdminToken != "legacy" {
		t.Fatalf("legacy fallback = read:%q admin:%q", legacy.ReadToken, legacy.AdminToken)
	}

	scoped := writeConfig(t, `{
		"token":"ingest",
		"read_token":"read",
		"admin_token":"admin"
	}`)
	if scoped.Token != "ingest" || scoped.ReadToken != "read" || scoped.AdminToken != "admin" {
		t.Fatalf("scoped tokens = ingest:%q read:%q admin:%q", scoped.Token, scoped.ReadToken, scoped.AdminToken)
	}

	explicitEmpty := writeConfig(t, `{
		"token":"legacy",
		"read_token":"",
		"admin_token":""
	}`)
	if explicitEmpty.ReadToken != "" || explicitEmpty.AdminToken != "" {
		t.Fatalf("explicitly empty scoped tokens must not fall back: read:%q admin:%q", explicitEmpty.ReadToken, explicitEmpty.AdminToken)
	}
}

func TestScopedTokensAuthorizeOnlyTheirOwnOperations(t *testing.T) {
	s := scopedSrv("192.168.1.10:8787", "ingest", "read", "admin")
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }

	cases := []struct {
		name       string
		handler    http.HandlerFunc
		token      string
		wantStatus int
	}{
		{name: "read token reads", handler: s.readAuth(ok), token: "read", wantStatus: http.StatusTeapot},
		{name: "ingest token cannot read", handler: s.readAuth(ok), token: "ingest", wantStatus: http.StatusUnauthorized},
		{name: "admin token cannot read", handler: s.readAuth(ok), token: "admin", wantStatus: http.StatusUnauthorized},
		{name: "ingest token ingests v1", handler: s.auth(ok), token: "ingest", wantStatus: http.StatusTeapot},
		{name: "read token cannot ingest v1", handler: s.auth(ok), token: "read", wantStatus: http.StatusUnauthorized},
		{name: "admin token cannot ingest v1", handler: s.auth(ok), token: "admin", wantStatus: http.StatusUnauthorized},
		{name: "admin token writes settings", handler: s.adminAuth(ok), token: "admin", wantStatus: http.StatusTeapot},
		{name: "read token cannot write settings", handler: s.adminAuth(ok), token: "read", wantStatus: http.StatusUnauthorized},
		{name: "ingest token cannot write settings", handler: s.adminAuth(ok), token: "ingest", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/v1/x", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			tc.handler(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestReachableServerRequiresReadAndAdminTokens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ingest string
		read   string
		admin  string
		ok     bool
	}{
		{name: "all present", ingest: "ingest", read: "read", admin: "admin", ok: true},
		{name: "missing ingest", read: "read", admin: "admin"},
		{name: "missing read", ingest: "ingest", admin: "admin"},
		{name: "missing admin", ingest: "ingest", read: "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scopedSrv("0.0.0.0:8787", tc.ingest, tc.read, tc.admin).requireAuthConsistency()
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestV2DeviceAuthenticationIsBoundToEnvelopeDeviceID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const deviceA = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	const deviceB = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a002"
	if _, err := st.RegisterDevice(deviceA, "A", "device-a-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterDevice(deviceB, "B", "device-b-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}

	request := func(token string) *http.Request {
		req := httptest.NewRequest("POST", "/api/v2/ingest", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}

	record, ok, err := s.authenticateIngestV2(
		request("device-a-token"),
		model.IngestEnvelopeV2{DeviceID: deviceA},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || record.DeviceID != deviceA {
		t.Fatalf("device A authentication = record:%#v ok:%v", record, ok)
	}

	_, ok, err = s.authenticateIngestV2(
		request("device-b-token"),
		model.IngestEnvelopeV2{DeviceID: deviceA},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("device B credential impersonated envelope device A")
	}

	if err := st.RevokeDevice(deviceA, 99); err != nil {
		t.Fatal(err)
	}
	_, ok, err = s.authenticateIngestV2(
		request("device-a-token"),
		model.IngestEnvelopeV2{DeviceID: deviceA},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("revoked device authenticated for v2 ingest")
	}
}

func TestV2BearerParserIsStrictAndSchemeIsCaseInsensitive(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const deviceID = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	if _, err := st.RegisterDevice(deviceID, "A", "device-token", nil, 10); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	envelope := model.IngestEnvelopeV2{DeviceID: deviceID}

	for _, tc := range []struct {
		name   string
		header string
		ok     bool
	}{
		{name: "canonical scheme", header: "Bearer device-token", ok: true},
		{name: "lowercase scheme", header: "bearer device-token", ok: true},
		{name: "uppercase scheme", header: "BEARER device-token", ok: true},
		{name: "missing header"},
		{name: "empty credential", header: "Bearer "},
		{name: "wrong scheme", header: "Basic device-token"},
		{name: "missing separator", header: "Bearerdevice-token"},
		{name: "multiple separators", header: "Bearer  device-token"},
		{name: "trailing material", header: "Bearer device-token extra"},
		{name: "leading whitespace", header: " Bearer device-token"},
		{name: "trailing whitespace", header: "Bearer device-token "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v2/ingest", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			_, ok, err := s.authenticateIngestV2(req, envelope)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.ok {
				t.Fatalf("authentication = %v, want %v for %q", ok, tc.ok, tc.header)
			}
		})
	}
}
