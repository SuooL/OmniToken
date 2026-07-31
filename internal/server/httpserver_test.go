package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
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
	if srv.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout = %v, want 60s for non-stream responses", srv.WriteTimeout)
	}
}

type streamTestWriter struct {
	header      http.Header
	mu          sync.Mutex
	body        strings.Builder
	cancel      context.CancelFunc
	writeLimits []time.Time
}

func (w *streamTestWriter) Header() http.Header { return w.header }

func (w *streamTestWriter) WriteHeader(int) {}

func (w *streamTestWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.body.Write(data)
	if strings.Count(w.body.String(), "event: ") >= 2 {
		w.cancel()
	}
	return n, err
}

func (w *streamTestWriter) Flush() {}

func (w *streamTestWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeLimits = append(w.writeLimits, deadline)
	return nil
}

func TestStreamDisablesPerRequestDeadlineAndRefreshesPayload(t *testing.T) {
	s := newLiveTestServer(t)
	s.streamRefreshInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	writer := &streamTestWriter{header: make(http.Header), cancel: cancel}
	done := make(chan struct{})
	go func() {
		s.handleStream(writer, httptestRequest(ctx, "/api/v1/stream"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not publish a periodic state-bearing refresh")
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.writeLimits) == 0 || !writer.writeLimits[0].IsZero() {
		t.Fatalf("stream write deadlines = %v, want an explicit zero deadline", writer.writeLimits)
	}
	body := writer.body.String()
	if !strings.Contains(body, "event: snapshot") || !strings.Contains(body, "event: live") {
		t.Fatalf("stream body = %q, want snapshot followed by live refresh", body)
	}
}

func httptestRequest(ctx context.Context, target string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	return req
}
