package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMergedRegisteredDeviceHasOneFleetRow(t *testing.T) {
	s := newLiveTestServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	for _, id := range []string{testV2DeviceA, testV2DeviceB} {
		if _, err := s.store.RegisterDevice(id, "pathzl", "token-"+id, nil, now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		seedEventOn(t, s, "event-"+id, id, now)
	}
	if err := s.store.RevokeDevice(testV2DeviceA, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TouchDevice(testV2DeviceB, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// A shared display name alone must never combine independent principals.
	before, err := s.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(before)
	var live struct {
		Devices []struct {
			Device, State string
			TodayEvents   int64 `json:"today_events"`
		}
	}
	if err := json.Unmarshal(encoded, &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Devices) != 2 {
		t.Fatalf("unconfirmed names combined: %s", encoded)
	}
	if _, err := s.store.MergeDeviceIdentity(testV2DeviceA, testV2DeviceB, "admin", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	after, err := s.livePayload(now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(after)
	if err := json.Unmarshal(encoded, &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Devices) != 1 || live.Devices[0].Device != testV2DeviceB || live.Devices[0].State != "active" || live.Devices[0].TodayEvents != 2 {
		t.Fatalf("merged fleet = %s", encoded)
	}
	recorder := httptest.NewRecorder()
	s.handleDevices(recorder, httptest.NewRequest("GET", "/api/v1/devices?days=30", nil))
	var body struct{ Summary []deviceEntry }
	decodeJSON(t, recorder, &body)
	if len(body.Summary) != 1 || body.Summary[0].Device != testV2DeviceB || body.Summary[0].Events != 2 || body.Summary[0].ConnectionState != "online" {
		t.Fatalf("merged summary = %s", recorder.Body.String())
	}
	old, err := s.store.DeviceByID(testV2DeviceA)
	if err != nil || old.RevokedAt == 0 {
		t.Fatalf("revocation lost: %+v %v", old, err)
	}
}
