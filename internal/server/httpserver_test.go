package server

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerHasBoundedConnectionTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:8787", http.NewServeMux())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %v", srv.ReadTimeout)
	}
	if srv.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %v", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 so SSE is not terminated", srv.WriteTimeout)
	}
}
