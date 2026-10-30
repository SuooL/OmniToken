package agent

import "testing"

func TestMachineIdentitySurvivesFreshConfig(t *testing.T) {
	a, err := PrepareEnrollment(FileConfig{}, "https://hub", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := PrepareEnrollment(FileConfig{}, "https://hub", "renamed", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.DeviceID != b.DeviceID {
		t.Fatalf("fresh enrollment changed machine ID: %s != %s", a.DeviceID, b.DeviceID)
	}
	if a.DeviceToken == b.DeviceToken {
		t.Fatal("credential must remain independent of machine identity")
	}
}

func TestMachineIdentityNormalizationAndInvalidValues(t *testing.T) {
	a, err := machineIdentityFromRaw(" 01234567-89AB-CDEF-0123-456789ABCDEF\n")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := machineIdentityFromRaw("0123456789abcdef0123456789abcdef")
	c, _ := machineIdentityFromRaw("1123456789abcdef0123456789abcdef")
	if a != b || a == c || a == "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatal("machine key is not normalized, distinct and namespaced")
	}
	for _, raw := range []string{"", "unknown", "00000000-0000-0000-0000-000000000000", "ffffffffffffffffffffffffffffffff"} {
		if _, err := machineIdentityFromRaw(raw); err == nil {
			t.Fatalf("accepted invalid identifier %q", raw)
		}
	}
}
