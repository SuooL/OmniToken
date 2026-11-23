package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestWave3BackendEnablers(t *testing.T) {
	srv := newLiveTestServer(t)
	srv.cfg.Listen = "127.0.0.1:8787"
	srv.cfg.Version = "v0.7.0"
	h := srv.routes()

	now := time.Now().UTC()
	devID := "11111111-1111-4111-8111-111111111111"
	if _, err := srv.store.RegisterDevice(devID, "Mac Studio", "dev-secret", []string{"events", "quotas"}, now.UnixMilli()); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if err := srv.store.TouchDevice(devID, now.UnixMilli()); err != nil {
		t.Fatalf("TouchDevice: %v", err)
	}
	if err := srv.store.SaveHeartbeat(model.Heartbeat{
		ProtocolVersion: model.IngestProtocolV2,
		DeviceID:        devID,
		AgentVersion:    "v0.6.1",
		QueuedBatches:   2,
		QueuedBytes:     4096,
		LastScanAt:      now.Add(-15 * time.Second).UnixMilli(),
		LastUploadAt:    now.Add(-5 * time.Second).UnixMilli(),
		Warnings:        []string{"disk_low"},
	}, now.UnixMilli()); err != nil {
		t.Fatalf("SaveHeartbeat: %v", err)
	}
	if _, err := srv.store.InsertEvents([]model.Event{{
		EventID:      "ev-w3-1",
		TS:           now.Add(-2 * time.Minute).UnixMilli(),
		Device:       devID,
		Source:       "claude-code",
		Provider:     model.ProviderAnthropicOAuth,
		Model:        "claude-sonnet-4-5",
		SessionID:    "sess-w3-1",
		Repo:         "SuooL/OmniToken",
		InputTokens:  1000,
		OutputTokens: 250,
	}}, now.UnixMilli()); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	// 1. /api/v1/health exposes hub_version.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["hub_version"] != "v0.7.0" {
		t.Fatalf("health hub_version = %v, want v0.7.0", health["hub_version"])
	}

	// 2. /api/v1/devices exposes hub_version + per-device agent_version/scan/upload/warnings.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/devices?days=30", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("devices status = %d", rec.Code)
	}
	var devResp struct {
		HubVersion string        `json:"hub_version"`
		Summary    []deviceEntry `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &devResp); err != nil {
		t.Fatal(err)
	}
	if devResp.HubVersion != "v0.7.0" || len(devResp.Summary) != 1 {
		t.Fatalf("devices resp = %+v", devResp)
	}
	gotDev := devResp.Summary[0]
	if gotDev.AgentVersion != "v0.6.1" || gotDev.LastScanAt == 0 || gotDev.LastUploadAt == 0 || len(gotDev.Warnings) != 1 {
		t.Fatalf("device metadata missing: %+v", gotDev)
	}

	// 3. /api/v1/reports?granularity=session exposes device_display_name.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reports?granularity=session&days=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reports status = %d", rec.Code)
	}
	var repResp struct {
		Rows []sessionReportRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &repResp); err != nil {
		t.Fatal(err)
	}
	if len(repResp.Rows) != 1 || repResp.Rows[0].DeviceDisplayName != "Mac Studio" {
		t.Fatalf("session report display name = %+v", repResp.Rows)
	}

	// 4. /api/v1/events?format=csv and format=json export filtered events.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/events?days=7&source=claude-code&format=csv", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "omnitoken-events.csv") {
		t.Fatalf("events csv status=%d disp=%q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][0] != "ev-w3-1" || rows[1][3] != "Mac Studio" {
		t.Fatalf("events csv rows = %+v, err=%v", rows, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/events?days=7&source=codex&format=json", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "omnitoken-events.json") {
		t.Fatalf("events json status=%d disp=%q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	var evExport struct {
		Total    int             `json:"total"`
		Exported int             `json:"exported"`
		Events   []eventWithCost `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &evExport); err != nil {
		t.Fatal(err)
	}
	if evExport.Total != 0 || evExport.Exported != 0 {
		t.Fatalf("filtered events json export = %+v", evExport)
	}

	// 5. /api/v1/settings exposes registered_devices for revocation UI.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings status = %d", rec.Code)
	}
	var setResp settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &setResp); err != nil {
		t.Fatal(err)
	}
	if len(setResp.RegisteredDevices) != 1 || setResp.RegisteredDevices[0].DeviceID != devID || setResp.RegisteredDevices[0].DisplayName != "Mac Studio" {
		t.Fatalf("settings registered_devices = %+v", setResp.RegisteredDevices)
	}
}
