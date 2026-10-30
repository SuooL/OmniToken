package server

import (
	"github.com/suool/omnitoken/internal/store"
	"net/http/httptest"
	"testing"
)

func TestMachineEnrollmentAdminScopeAndCanonicalID(t *testing.T) {
	s, st := newHeartbeatTestServer(t)
	if _, err := st.RegisterDevice(testV2DeviceA, "old", "old-token", nil, 1); err != nil {
		t.Fatal(err)
	}
	req := enrollmentV2Request{MachineID: testV2DeviceB, DeviceID: testV2DeviceA, DisplayName: "pathzl", DeviceToken: "old-token"}
	denied := httptest.NewRecorder()
	s.handleEnrollV2(denied, jsonRequest(t, "POST", "/api/v2/enroll", req, "old-token"))
	if denied.Code != 401 {
		t.Fatalf("device credential can bind machine: %d", denied.Code)
	}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		s.handleEnrollV2(rec, jsonRequest(t, "POST", "/api/v2/enroll", req, "admin-secret"))
		var response store.DeviceRecord
		decodeJSON(t, rec, &response)
		if response.DeviceID != testV2DeviceA {
			t.Fatalf("changed canonical ID: %+v", response)
		}
		req.DeviceID = testV2DeviceB
		req.DeviceToken = "new-token"
	}
	if _, ok, _ := st.AuthenticateDevice(testV2DeviceA, "new-token"); !ok {
		t.Fatal("new credential invalid")
	}
	if _, ok, _ := st.AuthenticateDevice(testV2DeviceA, "old-token"); ok {
		t.Fatal("old credential not rotated")
	}
}
