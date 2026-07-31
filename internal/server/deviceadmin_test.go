package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

func TestAdminCanRevokeDeviceThroughHTTP(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.AdminToken = "admin-secret"
	s.now = func() time.Time { return time.UnixMilli(9_000) }
	if _, err := s.store.RegisterDevice(testV2DeviceA, "Agent", "device-secret", nil, 1_000); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v2/devices/"+testV2DeviceA+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}

	record, err := s.store.DeviceByID(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if record.RevokedAt != 9_000 {
		t.Fatalf("revoked_at = %d, want server time 9000", record.RevokedAt)
	}
	if _, ok, err := s.store.AuthenticateDevice(testV2DeviceA, "device-secret"); err != nil || ok {
		t.Fatalf("revoked credential authenticated: ok=%v err=%v", ok, err)
	}
}

func TestDeviceRevocationRequiresAdminCredentialAndKnownCanonicalID(t *testing.T) {
	s := newLiveTestServer(t)
	s.cfg.AdminToken = "admin-secret"
	if _, err := s.store.RegisterDevice(testV2DeviceA, "Agent", "device-secret", nil, 1_000); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		id     string
		token  string
		status int
	}{
		{name: "missing admin", id: testV2DeviceA, status: http.StatusUnauthorized},
		{name: "device credential", id: testV2DeviceA, token: "device-secret", status: http.StatusUnauthorized},
		{name: "malformed id", id: "not-a-device", token: "admin-secret", status: http.StatusBadRequest},
		{name: "unknown id", id: testV2DeviceB, token: "admin-secret", status: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v2/devices/"+tc.id+"/revoke", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d body=%q, want %d", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}

	record, err := s.store.DeviceByID(testV2DeviceA)
	if err != nil {
		t.Fatal(err)
	}
	if record.RevokedAt != 0 {
		t.Fatalf("failed revocations changed record: %#v", record)
	}
}

func TestRevokeDeviceNotFoundMapsStoreError(t *testing.T) {
	s := newLiveTestServer(t)
	err := s.store.RevokeDevice(testV2DeviceA, 1)
	if err != store.ErrDeviceNotFound {
		t.Fatalf("error = %v, want ErrDeviceNotFound", err)
	}
}
