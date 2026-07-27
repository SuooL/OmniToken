package collect

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseUsageResponseFlatBuckets(t *testing.T) {
	body := []byte(`{
		"five_hour": {"utilization": 42.5, "resets_at": "2026-07-27T08:00:00Z"},
		"seven_day": {"utilization": 63, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_opus": {"utilization": 12, "resets_at": "2026-08-01T00:00:00Z"},
		"seven_day_sonnet": {"utilization": 0, "resets_at": null}
	}`)
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	qs, err := parseUsageResponse(body, "dev1", now)
	if err != nil {
		t.Fatal(err)
	}
	// the 0%/no-reset sonnet bucket is a placeholder and must be dropped
	if len(qs) != 3 {
		t.Fatalf("want 3 snapshots, got %d: %+v", len(qs), qs)
	}
	byScope := map[string]int{}
	for i, q := range qs {
		byScope[q.Scope] = i
		if q.Source != "claude-code" || q.Device != "dev1" || q.ObservedAt != now.UnixMilli() {
			t.Errorf("attribution wrong: %+v", q)
		}
	}
	f := qs[byScope["five_hour"]]
	if f.WindowMinutes != 300 || f.UsedPercent != 42.5 {
		t.Errorf("five_hour wrong: %+v", f)
	}
	want := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC).UnixMilli()
	if f.ResetsAt != want {
		t.Errorf("resets_at = %d, want %d", f.ResetsAt, want)
	}
	if w := qs[byScope["seven_day"]]; w.WindowMinutes != 10080 || w.UsedPercent != 63 {
		t.Errorf("seven_day wrong: %+v", w)
	}
}

func TestParseUsageResponseLimitsArray(t *testing.T) {
	body := []byte(`{
		"limits": [
			{"kind": "five_hour", "percent": 30, "resets_at": "2026-07-27T08:00:00Z"},
			{"kind": "weekly", "percent": 55, "resets_at": "2026-08-01T00:00:00Z"},
			{"kind": "weekly_scoped", "percent": 20, "resets_at": "2026-08-01T00:00:00Z",
			 "scope": {"model": {"display_name": "Claude Opus 4.8"}}}
		]
	}`)
	qs, err := parseUsageResponse(body, "d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(qs), qs)
	}
	found := false
	for _, q := range qs {
		if q.Scope == "seven_day:claude opus 4.8" && q.UsedPercent == 20 {
			found = true
		}
	}
	if !found {
		t.Errorf("weekly_scoped model quota missing: %+v", qs)
	}
}

func TestFlatBucketsWinOverLimitsArray(t *testing.T) {
	body := []byte(`{
		"five_hour": {"utilization": 42, "resets_at": "2026-07-27T08:00:00Z"},
		"limits": [{"kind": "five_hour", "percent": 99, "resets_at": "2026-07-27T08:00:00Z"}]
	}`)
	qs, _ := parseUsageResponse(body, "d", time.Now())
	if len(qs) != 1 || qs[0].UsedPercent != 42 {
		t.Errorf("flat bucket must win: %+v", qs)
	}
}

func TestParseCredentialToken(t *testing.T) {
	if got := parseCredentialToken([]byte(`{"claudeAiOauth":{"accessToken":"tok-123"}}`)); got != "tok-123" {
		t.Errorf("token = %q", got)
	}
	if got := parseCredentialToken([]byte(`{"other":1}`)); got != "" {
		t.Errorf("missing token must be empty, got %q", got)
	}
	if got := parseCredentialToken([]byte(`not json`)); got != "" {
		t.Errorf("bad json must be empty, got %q", got)
	}
}

func TestFetchSkipsApiKeyBilling(t *testing.T) {
	// API-key billing has no 5h/weekly window: the poller must not call the
	// endpoint at all, even if OAuth credentials are lying around.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := NewClaudeQuotaPoller("dev", func() string { return AuthAnthropicAPI })
	p.url, p.tokenFn = srv.URL, func() string { return "tok" }

	qs, err := p.Fetch(time.Now())
	if err != nil || qs != nil {
		t.Fatalf("api-key billing must yield no quota: qs=%+v err=%v", qs, err)
	}
	if called {
		t.Error("api-key billing must not hit the usage endpoint")
	}
	if !p.nextAllowed.IsZero() {
		t.Error("skipping must not arm the backoff timer")
	}
}

func TestFetchSubscriptionSendsAuthHeaders(t *testing.T) {
	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		w.Write([]byte(`{"five_hour":{"utilization":42,"resets_at":"2026-07-27T08:00:00Z"}}`))
	}))
	defer srv.Close()
	p := NewClaudeQuotaPoller("dev", func() string { return AuthAnthropicOAuth })
	p.url, p.tokenFn = srv.URL, func() string { return "tok-abc" }

	qs, err := p.Fetch(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].UsedPercent != 42 {
		t.Fatalf("quota not parsed: %+v", qs)
	}
	if gotAuth != "Bearer tok-abc" || gotBeta != usageAPIBeta {
		t.Errorf("headers wrong: auth=%q beta=%q", gotAuth, gotBeta)
	}
}

func TestFetchRateLimitBacksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewClaudeQuotaPoller("dev", nil)
	p.url, p.tokenFn = srv.URL, func() string { return "tok" }

	now := time.Now()
	if _, err := p.Fetch(now); err == nil {
		t.Fatal("429 must report an error")
	}
	if want := now.Add(2 * time.Minute); p.nextAllowed.Before(want.Add(-time.Second)) {
		t.Errorf("backoff = %v, want >= %v (Retry-After honoured)", p.nextAllowed, want)
	}
	// A second call inside the backoff window must not hit the server.
	if qs, err := p.Fetch(now.Add(time.Second)); qs != nil || err != nil {
		t.Errorf("call during backoff must be a no-op: %+v %v", qs, err)
	}
}
