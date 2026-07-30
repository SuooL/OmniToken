package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The rule these tests pin down (ADR-0016): whether a read needs a credential is
// derived from the listen address, never from a flag someone has to remember.
func srv(listen, token string) *Server {
	return &Server{cfg: &Config{Listen: listen, Token: token}}
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
