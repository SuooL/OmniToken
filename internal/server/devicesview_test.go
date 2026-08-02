package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func deviceIdentityStatuses(t *testing.T, s *Server) map[string]string {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.handleDevices(recorder, httptest.NewRequest("GET", "/api/v1/devices?days=30", nil))
	var payload struct {
		Summary []struct {
			Device         string `json:"device"`
			IdentityStatus string `json:"identity_status"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode devices payload: %v", err)
	}
	out := map[string]string{}
	for _, row := range payload.Summary {
		out[row.Device] = row.IdentityStatus
	}
	return out
}

// The machine running the hub is not a device that forgot to enrol.
//
// It has no agent, no token and no heartbeat because it needs none: the server
// scans its own logs directly. Calling it `legacy_unbound` put it in the same
// bucket as an agent still speaking v1 and pointed the reader at an upgrade
// that does not exist — and it could never leave that bucket, because there is
// nothing there to enrol. On a real panel it was the only row left wearing the
// label, every other device having since registered.
func TestLocalMachineIsNotLabelledLegacy(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DeviceName = "hub-mac"
	now := time.Now()
	seedEventOn(t, s, "local-1", "hub-mac", now)
	seedEventOn(t, s, "remote-1", "other-box", now)

	status := deviceIdentityStatuses(t, s)
	if got := status["hub-mac"]; got != "local" {
		t.Errorf("the hub's own machine is %q, want %q", got, "local")
	}
	// A device that really has not enrolled keeps saying so.
	if got := status["other-box"]; got != "legacy_unbound" {
		t.Errorf("a remote unenrolled device is %q, want legacy_unbound", got)
	}
}

// With local collection switched off, a row under the hub's own name is another
// machine reporting in — the hub is not scanning anything itself, so the label
// would be a claim about a collector that is not running.
func TestLocalLabelRequiresLocalCollection(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DeviceName = "hub-mac"
	off := false
	s.cfg.Collect.Local = &off
	seedEventOn(t, s, "e1", "hub-mac", time.Now())

	if got := deviceIdentityStatuses(t, s)["hub-mac"]; got != "legacy_unbound" {
		t.Errorf("identity_status = %q with local collection off, want legacy_unbound", got)
	}
}

// An enrolled device keeps its registered identity even if it happens to share
// the hub's configured name: registration is evidence, a name is not.
func TestRegisteredDeviceOutranksTheLocalLabel(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.DeviceName = "hub-mac"
	if _, err := s.store.RegisterDevice(testV2DeviceA, "hub-mac", "device-token", []string{"events"}, 1); err != nil {
		t.Fatal(err)
	}
	seedEventOn(t, s, "e1", testV2DeviceA, time.Now())

	if got := deviceIdentityStatuses(t, s)[testV2DeviceA]; got != "registered" {
		t.Errorf("identity_status = %q, want registered", got)
	}
}
