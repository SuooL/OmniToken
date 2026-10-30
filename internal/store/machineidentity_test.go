package store

import "testing"

func TestMachineEnrollmentKeepsLegacyIdentityAcrossReinstall(t *testing.T) {
	s := openTestStore(t)
	const old = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	const machine = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a002"
	if _, err := s.RegisterDevice(old, "pathzl", "old-token", nil, 1); err != nil {
		t.Fatal(err)
	}
	first, err := s.EnrollMachineDevice(machine, old, "pathzl", "old-token", nil, 2)
	if err != nil || first.DeviceID != old {
		t.Fatalf("bind: %+v %v", first, err)
	}
	second, err := s.EnrollMachineDevice(machine, machine, "renamed", "new-token", nil, 3)
	if err != nil || second.DeviceID != old {
		t.Fatalf("reinstall: %+v %v", second, err)
	}
	if _, ok, _ := s.AuthenticateDevice(old, "old-token"); ok {
		t.Fatal("old credential still valid")
	}
	if _, ok, _ := s.AuthenticateDevice(old, "new-token"); !ok {
		t.Fatal("new credential rejected")
	}
	records, _ := s.ListDevices()
	if len(records) != 1 {
		t.Fatalf("duplicate principals: %+v", records)
	}
	if err := s.RevokeDevice(old, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollMachineDevice(machine, machine, "pathzl", "third-token", nil, 5); err == nil {
		t.Fatal("revoked principal reactivated")
	}
}

func TestMachineEnrollmentBindingConflictRollsBack(t *testing.T) {
	s := openTestStore(t)
	const id = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a001"
	const first = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a002"
	const other = "018f2d5a-7b31-7d98-bf8e-3c2f35a1a003"
	if _, err := s.EnrollMachineDevice(first, id, "original", "token", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollMachineDevice(other, id, "incorrect", "token", nil, 2); err == nil {
		t.Fatal("one principal bound to two machines")
	}
	record, _ := s.DeviceByID(id)
	if record.DisplayName != "original" {
		t.Fatalf("failed binding changed metadata: %+v", record)
	}
}
