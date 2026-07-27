package statusline

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionJSON = `{"model":{"display_name":"Opus 4.8"},
 "cost":{"total_cost_usd":1.25,"total_duration_ms":600000},
 "context_window":{"total_input_tokens":12000,"total_output_tokens":3000}}`

func fakeServer(t *testing.T, quotaPct float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/overview"):
			json.NewEncoder(w).Encode(map[string]any{
				"today":     map[string]any{"total_tokens": 5_400_000},
				"costs":     map[string]any{"today": map[string]any{"real_usd": 2.0, "equivalent_usd": 8.0}},
				"by_device": []map[string]string{{"key": "mac"}, {"key": "srv1"}},
			})
		case r.URL.Path == "/api/v1/quota":
			json.NewEncoder(w).Encode(map[string]any{"quotas": []map[string]any{
				{"source": "claude-code", "scope": "five_hour", "used_percent": quotaPct,
					"resets_at": time.Now().Add(68 * time.Minute).UnixMilli(), "expired": false},
				{"source": "claude-code", "scope": "seven_day", "used_percent": 36,
					"resets_at": time.Now().Add(70 * time.Hour).UnixMilli(), "expired": false},
				{"source": "claude-code", "scope": "seven_day_opus", "used_percent": 99,
					"resets_at": time.Now().Add(70 * time.Hour).UnixMilli(), "expired": false},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func runLine(t *testing.T, cfg Config, stdin string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(cfg, strings.NewReader(stdin), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return strings.TrimRight(out.String(), "\n")
}

func TestRenderFull(t *testing.T) {
	srv := fakeServer(t, 97)
	defer srv.Close()
	cfg := Config{Server: srv.URL, CachePath: filepath.Join(t.TempDir(), "c.json"), NoColor: true}

	line := runLine(t, cfg, sessionJSON)
	for _, want := range []string{"Opus 4.8", "15.0K", "$1.25", "今日 5.4M", "$10.00", "(2 台)", "5h 97%", "周 36%"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %s", want, line)
		}
	}
	// Only the top-level windows belong on a status line, not per-model ones.
	if strings.Contains(line, "99%") {
		t.Errorf("per-model quota must not be shown: %s", line)
	}
	if strings.Contains(line, staleMark) {
		t.Errorf("fresh fetch must not be marked stale: %s", line)
	}
}

func TestServerDownUsesCacheAndMarksStale(t *testing.T) {
	srv := fakeServer(t, 50)
	cache := filepath.Join(t.TempDir(), "c.json")
	cfg := Config{Server: srv.URL, CachePath: cache, NoColor: true}
	runLine(t, cfg, sessionJSON) // warm the cache
	srv.Close()

	// Age the cache past the freshness window so a live fetch is attempted.
	var d serverData
	raw, _ := os.ReadFile(cache)
	json.Unmarshal(raw, &d)
	d.FetchedAt = time.Now().Add(-time.Hour)
	out, _ := json.Marshal(d)
	os.WriteFile(cache, out, 0o644)

	line := runLine(t, cfg, sessionJSON)
	if !strings.Contains(line, "今日 5.4M") {
		t.Errorf("cached data must still render: %s", line)
	}
	if !strings.Contains(line, staleMark) {
		t.Errorf("stale data must be marked with %q: %s", staleMark, line)
	}
}

func TestNoServerNoCacheStillRendersSession(t *testing.T) {
	cfg := Config{Server: "http://127.0.0.1:1", CachePath: filepath.Join(t.TempDir(), "c.json"), NoColor: true}
	line := runLine(t, cfg, sessionJSON)
	if !strings.Contains(line, "Opus 4.8") {
		t.Errorf("session data must survive a dead server: %s", line)
	}
	if strings.Contains(line, "今日") {
		t.Errorf("must not invent cross-device data: %s", line)
	}
}

func TestNoQuotaSegmentWhenNoneAuthoritative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/overview") {
			json.NewEncoder(w).Encode(map[string]any{"today": map[string]any{"total_tokens": 100}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"quotas": []any{}})
	}))
	defer srv.Close()
	cfg := Config{Server: srv.URL, CachePath: filepath.Join(t.TempDir(), "c.json"), NoColor: true}
	line := runLine(t, cfg, sessionJSON)
	if strings.Contains(line, "5h") || strings.Contains(line, "周") {
		t.Errorf("no authoritative quota → no quota segment (never infer): %s", line)
	}
}

func TestMalformedStdinDoesNotPanic(t *testing.T) {
	srv := fakeServer(t, 10)
	defer srv.Close()
	cfg := Config{Server: srv.URL, CachePath: filepath.Join(t.TempDir(), "c.json"), NoColor: true}
	for _, in := range []string{"", "not json", "[]", "{"} {
		if line := runLine(t, cfg, in); line == "" {
			t.Errorf("input %q produced an empty line", in)
		}
	}
}

func TestColorSuppression(t *testing.T) {
	srv := fakeServer(t, 97)
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "c.json")

	colored := runLine(t, Config{Server: srv.URL, CachePath: cache}, sessionJSON)
	if !strings.Contains(colored, "\033[") {
		t.Errorf("colour expected by default: %q", colored)
	}
	plain := runLine(t, Config{Server: srv.URL, CachePath: cache, NoColor: true}, sessionJSON)
	if strings.Contains(plain, "\033[") {
		t.Errorf("NoColor must strip ANSI: %q", plain)
	}
}

func TestFreshCacheSkipsNetwork(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.HasPrefix(r.URL.Path, "/api/v1/overview") {
			json.NewEncoder(w).Encode(map[string]any{"today": map[string]any{"total_tokens": 7}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"quotas": []any{}})
	}))
	defer srv.Close()
	cfg := Config{Server: srv.URL, CachePath: filepath.Join(t.TempDir(), "c.json"), NoColor: true}
	runLine(t, cfg, sessionJSON)
	before := hits
	runLine(t, cfg, sessionJSON) // within the freshness window
	if hits != before {
		t.Errorf("fresh cache must skip the network: %d extra hits", hits-before)
	}
}
