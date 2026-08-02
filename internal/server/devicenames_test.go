package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

func decodeJSON(t *testing.T, recorder *httptest.ResponseRecorder, out any) {
	t.Helper()
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func breakdownByDevice(t *testing.T, s *Server) map[string]string {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.handleBreakdown(recorder, httptest.NewRequest("GET", "/api/v1/breakdown?by=device&days=30", nil))
	var body struct {
		Rows []struct {
			Key         string `json:"key"`
			DisplayName string `json:"display_name"`
		} `json:"rows"`
	}
	decodeJSON(t, recorder, &body)
	names := map[string]string{}
	for _, row := range body.Rows {
		names[row.Key] = row.DisplayName
	}
	return names
}

// A v2 device is keyed by UUID, so every device-keyed view that prints the key
// alone prints 36 characters of hex at the user.
func TestDeviceBreakdownCarriesTheNameThePanelShouldPrint(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	if _, err := s.store.RegisterDevice(testV2DeviceA, "mypc", "device-token", []string{"events"}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	seedEventOn(t, s, "registered-event", testV2DeviceA, now)
	seedEventOn(t, s, "legacy-event", "legacy-host", now)

	names := breakdownByDevice(t, s)
	if names[testV2DeviceA] != "mypc" {
		t.Fatalf("registered device display name = %q, want %q", names[testV2DeviceA], "mypc")
	}
	// A v1 identity already is its hostname: nothing to add, and inventing a
	// name here would make the panel show a label the settings page cannot edit.
	if got, ok := names["legacy-host"]; !ok || got != "" {
		t.Fatalf("legacy identity display name = %q (present=%v), want empty", got, ok)
	}
}

func TestNonDeviceBreakdownsAreUntouched(t *testing.T) {
	s := newLiveTestServer(t)
	seedEventOn(t, s, "an-event", "legacy-host", time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local))
	recorder := httptest.NewRecorder()
	s.handleBreakdown(recorder, httptest.NewRequest("GET", "/api/v1/breakdown?by=model&days=30", nil))
	var body struct {
		Rows []map[string]any `json:"rows"`
	}
	decodeJSON(t, recorder, &body)
	if len(body.Rows) != 1 {
		t.Fatalf("rows = %#v", body.Rows)
	}
	if _, present := body.Rows[0]["display_name"]; present {
		t.Fatalf("model row carries a device display name: %#v", body.Rows[0])
	}
}

// The label is the operator's own word for a machine they own; the display name
// is what the machine said about itself. Nothing a machine self-reports outranks
// what the operator typed.
func TestTypedLabelOutranksSelfReportedDisplayName(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	if _, err := s.store.RegisterDevice(testV2DeviceA, "DESKTOP-86HNP05", "device-token", []string{"events"}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TouchDevice(testV2DeviceA, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	seedEventOn(t, s, "registered-event", testV2DeviceA, now)
	seedEventOn(t, s, "legacy-event", "legacy-host", now)
	if err := s.store.SetSettingsJSON(store.DeviceLabelsKey, map[string]string{
		testV2DeviceA: "书房台式机",
		"legacy-host": "客厅 mini",
	}); err != nil {
		t.Fatal(err)
	}

	names := breakdownByDevice(t, s)
	if names[testV2DeviceA] != "书房台式机" {
		t.Fatalf("labelled v2 device = %q", names[testV2DeviceA])
	}
	// A rename is the only way to name a v1 identity, so it must reach it too.
	if names["legacy-host"] != "客厅 mini" {
		t.Fatalf("labelled v1 identity = %q", names["legacy-host"])
	}

	// Every device-keyed view resolves through the same table, or the same
	// machine ends up with two names depending on which page you opened.
	recorder := httptest.NewRecorder()
	s.handleDevices(recorder, httptest.NewRequest("GET", "/api/v1/devices?days=30", nil))
	var devices struct {
		Summary []struct {
			Device      string `json:"device"`
			DisplayName string `json:"display_name"`
		} `json:"summary"`
	}
	decodeJSON(t, recorder, &devices)
	for _, row := range devices.Summary {
		switch row.Device {
		case testV2DeviceA:
			if row.DisplayName != "书房台式机" {
				t.Fatalf("devices page v2 name = %q", row.DisplayName)
			}
		case "legacy-host":
			if row.DisplayName != "客厅 mini" {
				t.Fatalf("devices page v1 name = %q", row.DisplayName)
			}
		}
	}

	live, err := s.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Devices []struct {
			Device      string `json:"device"`
			DisplayName string `json:"display_name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Devices {
		switch row.Device {
		case testV2DeviceA:
			if row.DisplayName != "书房台式机" {
				t.Fatalf("live view v2 name = %q", row.DisplayName)
			}
		case "legacy-host":
			if row.DisplayName != "客厅 mini" {
				t.Fatalf("live view v1 name = %q", row.DisplayName)
			}
		}
	}
}
