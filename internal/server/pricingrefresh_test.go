package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/pricing"
)

func pricingCatalogForTest() map[string]pricing.Price {
	models := map[string]pricing.Price{}
	for i := 0; i < 120; i++ {
		models[fmt.Sprintf("new-model-%d", i)] = pricing.Price{Input: 0.000001, Output: 0.000002}
	}
	return models
}

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testPricingClient(t *testing.T, good bool) *http.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !good {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(pricingCatalogForTest())
	}))
	t.Cleanup(ts.Close)
	base := ts.Client()
	return &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, ts.URL, nil)
		return base.Do(req)
	})}
}

func TestPricingRefreshPersistsReloadsAndKeepsOverrides(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DBPath = filepath.Join(t.TempDir(), "usage.db")
	s.cfg.PricingOverrides = map[string]pricing.Override{"NEW-MODEL-0": {InputPerM: 4}, "new-model-1": {InputPerM: 5}}
	if err := s.store.SetSettingsJSON(settingsKeyPricing, map[string]pricing.Override{"new-model-0": {InputPerM: 9}}); err != nil {
		t.Fatal(err)
	}
	s.readCache = newRespCache(time.Now)
	_, _ = s.cachedJSON("overview", time.Hour, func() ([]byte, error) { return []byte("old-price"), nil })
	if _, ok := s.Prices().Lookup("new-model-0"); ok {
		t.Fatal("fixture should begin unpriced")
	}
	if err := s.refreshPricing(context.Background(), testPricingClient(t, true)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]float64{"new-model-0": 9e-6, "new-model-1": 5e-6, "new-model-2": 1e-6} {
		if p, ok := s.Prices().Lookup(name); !ok || p.Input != want {
			t.Fatalf("%s price=%+v ok=%v want=%v", name, p, ok, want)
		}
	}
	body, _ := s.cachedJSON("overview", time.Hour, func() ([]byte, error) { return []byte("new-price"), nil })
	if string(body) != "new-price" {
		t.Fatal("stale cost cache survived")
	}
	status := s.pricingUpdateStatus()
	if !status.Enabled || status.LastSuccessMS == 0 || status.ModelCount != 120 || status.LastError != "" {
		t.Fatalf("status=%+v", status)
	}
	before, _ := os.ReadFile(s.pricingCachePath())
	if err := s.refreshPricing(context.Background(), testPricingClient(t, false)); err == nil {
		t.Fatal("expected refresh failure")
	}
	after, _ := os.ReadFile(s.pricingCachePath())
	failed := s.pricingUpdateStatus()
	if string(before) != string(after) || failed.LastSuccessMS != status.LastSuccessMS || failed.LastError == "" {
		t.Fatalf("failure lost previous snapshot: %+v", failed)
	}
	if err := s.ReloadPricing(); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Prices().Lookup("new-model-0"); p.Input != 9e-6 {
		t.Fatal("settings reload reverted catalog")
	}
	// Restart loads cached prices without an HTTP request.
	restart := newLiveTestServer(t)
	restart.cfg.DBPath = s.cfg.DBPath
	restart.loadPricingSnapshot()
	if err := restart.ReloadPricing(); err != nil {
		t.Fatal(err)
	}
	if p, ok := restart.Prices().Lookup("new-model-2"); !ok || p.Input != 1e-6 {
		t.Fatalf("offline price=%+v ok=%v", p, ok)
	}
	rec := httptest.NewRecorder()
	s.handleGetSettings(rec, httptest.NewRequest("GET", "/api/v1/settings", nil))
	var resp struct {
		Update pricingRefreshStatus `json:"pricing_update"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Update.LastSuccessMS == 0 || resp.Update.Source != pricing.CatalogURL {
		t.Fatalf("API status=%+v", resp.Update)
	}
}

func TestPricingRefreshDiskFailureKeepsLiveTable(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DBPath = filepath.Join(t.TempDir(), "missing", "usage.db")
	before := s.Prices()
	if err := s.refreshPricing(context.Background(), testPricingClient(t, true)); err == nil {
		t.Fatal("expected persistence failure")
	}
	if s.Prices() != before || s.pricingUpdateStatus().LastSuccessMS != 0 {
		t.Fatal("unsaved prices became live")
	}
}

func TestPricingScheduleAndDisabledMode(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now().Truncate(time.Millisecond)
	if delay := s.pricingRefreshDelay(now); delay != 0 {
		t.Fatalf("first refresh delay=%v", delay)
	}
	s.pricingStatus.LastAttemptMS = now.UnixMilli()
	s.pricingStatus.LastError = "offline on first boot"
	if delay := s.pricingRefreshDelay(now); delay != time.Hour {
		t.Fatalf("first-boot retry delay=%v", delay)
	}
	s.pricingStatus.LastError = ""
	s.pricingStatus.LastSuccessMS = now.UnixMilli()
	if delay := s.pricingRefreshDelay(now); delay != 24*time.Hour {
		t.Fatalf("fresh cache delay=%v", delay)
	}
	s.pricingStatus.LastAttemptMS = now.Add(time.Hour).UnixMilli()
	s.pricingStatus.LastError = "offline"
	if delay := s.pricingRefreshDelay(now.Add(time.Hour)); delay != time.Hour {
		t.Fatalf("retry delay=%v", delay)
	}
	off := false
	s.cfg.PricingAutoUpdate = &off
	if s.pricingUpdateStatus().Enabled {
		t.Fatal("disabled status")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.runPricingUpdates(ctx)
}

func TestPricingRefreshAndSettingsReloadAreRaceFree(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DBPath = filepath.Join(t.TempDir(), "usage.db")
	client := testPricingClient(t, true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				if err := s.ReloadPricing(); err != nil {
					t.Error(err)
				}
				s.Prices().Lookup("new-model-0")
				s.pricingUpdateStatus()
			}
		}()
	}
	if err := s.refreshPricing(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, ok := s.Prices().Lookup("new-model-0"); !ok {
		t.Fatal("reload lost refreshed catalog")
	}
}

func TestPricingSnapshotCorruptionFallsBackToEmbedded(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DBPath = filepath.Join(t.TempDir(), "usage.db")
	if err := os.WriteFile(s.pricingCachePath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s.loadPricingSnapshot()
	if err := s.ReloadPricing(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Prices().Lookup("gpt-5.5"); !ok {
		t.Fatal("embedded fallback missing")
	}
	if s.pricingUpdateStatus().LastError == "" {
		t.Fatal("corrupt cache not surfaced")
	}
}
