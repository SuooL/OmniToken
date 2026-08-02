package agent

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPClientNoResolveIP(t *testing.T) {
	c, err := newHTTPClient(5*time.Second, "https://hub.example", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Transport != nil {
		t.Fatal("without resolve_ip the client must keep the default transport")
	}
}

func TestNewHTTPClientRejectsBadIP(t *testing.T) {
	if _, err := newHTTPClient(5*time.Second, "https://hub.example", "not-an-ip"); err == nil {
		t.Fatal("expected an error for a non-IP resolve_ip")
	}
}

// The pin makes an otherwise-unresolvable hostname reachable by dialing the
// configured IP, which proves normal DNS is bypassed.
func TestNewHTTPClientPinsDNS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split test server URL: %v", err)
	}
	// .invalid is guaranteed never to resolve (RFC 2606).
	target := "http://hub.invalid:" + port

	// Control: without the pin the bogus host cannot be reached.
	if _, err := (&http.Client{Timeout: 3 * time.Second}).Get(target); err == nil {
		t.Fatal("control: expected the bogus host to fail without a pin")
	}

	c, err := newHTTPClient(3*time.Second, target, "127.0.0.1")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	resp, err := c.Get(target)
	if err != nil {
		t.Fatalf("pinned request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

// The security-critical property: dial the pinned IP but validate the
// certificate against the URL hostname. The httptest cert is valid for
// example.com (and 127.0.0.1); a request to https://example.com that connects
// to 127.0.0.1 must still verify against the hostname, not the dialed IP.
func TestNewHTTPClientPreservesTLSHostname(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatalf("split test server URL: %v", err)
	}
	target := "https://example.com:" + port

	c, err := newHTTPClient(3*time.Second, target, "127.0.0.1")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	// Trust the test cert. ServerName is left empty so it is derived from the
	// request URL (example.com) — exactly the value under test.
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}

	resp, err := c.Get(target)
	if err != nil {
		t.Fatalf("pinned TLS request failed (hostname not preserved?): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}
