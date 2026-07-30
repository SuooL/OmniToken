package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/model"
)

func TestDevicesMergesRegistryAndUsesOnlyServerLastSeenForConnectionState(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	devices := []struct {
		id       string
		name     string
		lastSeen time.Time
		state    string
	}{
		{id: testV2DeviceA, name: "Online Agent", lastSeen: now.Add(-time.Minute), state: "online"},
		{id: testV2DeviceB, name: "Stale Agent", lastSeen: now.Add(-5 * time.Minute), state: "stale"},
		{id: "018f2d5a-7b31-7d98-bf8e-3c2f35a1a003", name: "Offline Agent", lastSeen: now.Add(-20 * time.Minute), state: "offline"},
		{id: "018f2d5a-7b31-7d98-bf8e-3c2f35a1a004", name: "Never Seen", state: "offline"},
	}
	for i, device := range devices {
		if _, err := s.store.RegisterDevice(device.id, device.name, "device-token-"+device.id, []string{"events"}, int64(i+1)); err != nil {
			t.Fatal(err)
		}
		if !device.lastSeen.IsZero() {
			if err := s.store.TouchDevice(device.id, device.lastSeen.UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.store.SaveHeartbeat(model.Heartbeat{
		ProtocolVersion: 2,
		DeviceID:        testV2DeviceA,
		BootID:          testV2Boot,
		Sequence:        1,
		SentAt:          now.Add(24 * time.Hour).UnixMilli(),
		Capabilities:    []string{"events", "heartbeat"},
		QueuedBatches:   4,
		QueuedBytes:     512,
	}, devices[0].lastSeen.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	// A future client event must not make an offline registered device online.
	seedEventOn(t, s, "future-client-clock", devices[2].id, now.Add(24*time.Hour))
	// Usage-only legacy identities remain visible but cannot claim heartbeat
	// liveness because their event timestamps are client-controlled.
	seedEventOn(t, s, "legacy-event", "legacy-host", now)

	recorder := httptest.NewRecorder()
	s.handleDevices(recorder, httptest.NewRequest("GET", "/api/v1/devices?days=30", nil))
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Summary []deviceEntry `json:"summary"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	byDevice := make(map[string]deviceEntry, len(response.Summary))
	for _, entry := range response.Summary {
		byDevice[entry.Device] = entry
	}
	for _, device := range devices {
		entry, ok := byDevice[device.id]
		if !ok {
			t.Fatalf("registered device %s missing from %#v", device.id, response.Summary)
		}
		if entry.DeviceID != device.id || entry.DisplayName != device.name ||
			entry.ConnectionState != device.state || entry.IdentityStatus != "registered" {
			t.Fatalf("device %s entry = %#v", device.id, entry)
		}
	}
	if age := byDevice[testV2DeviceA].LastSeenAgeMS; age == nil || *age != time.Minute.Milliseconds() {
		t.Fatalf("online last-seen age = %v, want 60000", age)
	}
	if offline := byDevice[devices[2].id]; offline.ConnectionState != "offline" {
		t.Fatalf("future client timestamp changed liveness: %#v", offline)
	}
	if online := byDevice[testV2DeviceA]; online.QueuedBatches != 4 || online.QueuedBytes != 512 {
		t.Fatalf("heartbeat backlog missing: %#v", online)
	}
	legacy, ok := byDevice["legacy-host"]
	if !ok || legacy.IdentityStatus != "legacy_unbound" || legacy.ConnectionState != "unknown" {
		t.Fatalf("legacy compatibility entry = %#v, present=%v", legacy, ok)
	}
}
