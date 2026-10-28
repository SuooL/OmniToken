package server

import (
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/store"
	"net/http/httptest"
	"testing"
)

func TestEventPageCarriesReadableDeviceNameWithoutChangingIdentity(t *testing.T) {
	s := newLiveTestServer(t)
	now := insideLookBack()
	if _, err := s.store.RegisterDevice(testV2DeviceA, "Registered Mac", "device-token", []string{"events"}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	seedEventOn(t, s, "registered-event", testV2DeviceA, now)
	seedEventOn(t, s, "legacy-event", "legacy-host", now)
	for _, renamed := range []bool{false, true} {
		if renamed {
			if err := s.store.SetSettingsJSON(store.DeviceLabelsKey, map[string]string{testV2DeviceA: "书房 Mac"}); err != nil {
				t.Fatal(err)
			}
		}
		rec := httptest.NewRecorder()
		s.handleEvents(rec, httptest.NewRequest("GET", "/api/v1/events?days=30", nil))
		var body struct {
			Events []struct {
				model.Event
				Name string `json:"device_display_name"`
			} `json:"events"`
		}
		decodeJSON(t, rec, &body)
		if len(body.Events) != 2 {
			t.Fatalf("events=%+v", body.Events)
		}
		for _, e := range body.Events {
			if e.Device == testV2DeviceA {
				want := "Registered Mac"
				if renamed {
					want = "书房 Mac"
				}
				if e.Name != want {
					t.Fatalf("name=%q want=%q", e.Name, want)
				}
			} else if e.Device != "legacy-host" || e.Name != "" {
				t.Fatalf("unexpected identity/name: %+v", e)
			}
		}
	}
}
