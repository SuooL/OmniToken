package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCatalog() []byte {
	rows := map[string]any{"sample_spec": map[string]any{"input_cost_per_token": "documentation"}}
	for i := 0; i < 120; i++ {
		rows[fmt.Sprintf("new-model-%d", i)] = map[string]any{"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "context_window": 1000000}
	}
	rows["new-model-0"].(map[string]any)["cache_read_input_token_cost"] = nil
	b, _ := json.Marshal(rows)
	return b
}

func TestParseCatalogValidatesEntireCandidate(t *testing.T) {
	valid := testCatalog()
	catalog, err := ParseCatalog(valid)
	if err != nil || len(catalog) != 120 {
		t.Fatalf("models=%d err=%v", len(catalog), err)
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`null`), []byte(`[]`), []byte(`{"one":{"input_cost_per_token":1}}`), []byte(`{`),
		[]byte(strings.Replace(string(valid), `"input_cost_per_token":0.000001`, `"input_cost_per_token":-1`, 1)),
		[]byte(strings.Replace(string(valid), `"input_cost_per_token":0.000001`, `"input_cost_per_token":"free"`, 1)),
	} {
		if _, err := ParseCatalog(raw); err == nil {
			t.Fatalf("accepted invalid catalog: %.100s", raw)
		}
	}
}

func TestFetchCatalogBoundsAndHTTPFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		ok   bool
	}{
		{"valid", 200, string(testCatalog()), true}, {"http", 503, "unavailable", false}, {"malformed", 200, "nope", false},
		{"oversized", 200, strings.Repeat(" ", catalogMaxBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); _, _ = w.Write([]byte(tc.body)) }))
			defer ts.Close()
			client := ts.Client()
			client.Transport = redirectTransport{base: client.Transport, url: ts.URL}
			_, err := FetchCatalog(context.Background(), client)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchCatalog(ctx, http.DefaultClient); err == nil {
		t.Fatal("canceled fetch succeeded")
	}
}

type redirectTransport struct {
	base http.RoundTripper
	url  string
}

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, rt.url, nil)
	return rt.base.RoundTrip(req)
}

func TestSnapshotAtomicPersistenceAndOfflineLoad(t *testing.T) {
	models, _ := ParseCatalog(testCatalog())
	want := Snapshot{FetchedAt: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Models: models}
	path := filepath.Join(t.TempDir(), "pricing-cache.json")
	if err := SaveSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshot(path)
	if err != nil || !got.FetchedAt.Equal(want.FetchedAt) || len(got.Models) != len(models) {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	before, _ := os.ReadFile(path)
	if err := SaveSnapshot(path, Snapshot{FetchedAt: time.Now(), Models: map[string]Price{}}); err == nil {
		t.Fatal("saved empty snapshot")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("bad candidate replaced valid cache")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions")
	}
	if err := os.WriteFile(path, []byte(`{"fetched_at":"2026-10-28T00:00:00Z","models":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil {
		t.Fatal("loaded broken cache")
	}
}

func TestFetchCatalogFallsBackToOfficialGitHubAPI(t *testing.T) {
	var urls []string
	client := &http.Client{Transport: fetchTransport(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		if len(urls) == 1 {
			return nil, fmt.Errorf("raw host unavailable")
		}
		if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Error("API must return raw catalog")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(testCatalog()))), Header: make(http.Header)}, nil
	})}
	if _, err := FetchCatalog(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 || urls[0] != CatalogURL || urls[1] != catalogAPIURL {
		t.Fatalf("unexpected sources: %v", urls)
	}
}

type fetchTransport func(*http.Request) (*http.Response, error)

func (f fetchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
