package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnrollMachineUsesHubCanonicalIdentity(t *testing.T) {
	const id = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"device_id":"` + id + `"}`))
	}))
	defer hub.Close()
	fc, err := PrepareEnrollment(FileConfig{}, hub.URL, "pathzl", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Enroll(hub.URL, "admin", &fc, nil); err != nil {
		t.Fatal(err)
	}
	if fc.DeviceID != id {
		t.Fatalf("canonical ID not adopted: %s", fc.DeviceID)
	}
}
