package store

import (
	"testing"
)

func openSettingsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingMissingKey(t *testing.T) {
	s := openSettingsStore(t)

	// A key that was never written is not an error — callers apply defaults.
	v, err := s.GetSetting("nope")
	if err != nil {
		t.Fatalf("GetSetting on missing key: %v", err)
	}
	if v != "" {
		t.Errorf("GetSetting(missing) = %q, want empty", v)
	}
}

func TestSettingSetGetOverwrite(t *testing.T) {
	s := openSettingsStore(t)

	if err := s.SetSetting("device_labels", "alpha"); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSetting("device_labels")
	if err != nil {
		t.Fatal(err)
	}
	if v != "alpha" {
		t.Fatalf("GetSetting = %q, want alpha", v)
	}

	// Upsert: writing again replaces rather than erroring on the primary key.
	if err := s.SetSetting("device_labels", "beta"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if v, err = s.GetSetting("device_labels"); err != nil || v != "beta" {
		t.Fatalf("after overwrite = %q, %v; want beta", v, err)
	}

	// Empty value is a real value, distinct from "never set" only by intent.
	if err := s.SetSetting("device_labels", ""); err != nil {
		t.Fatal(err)
	}
	if v, err = s.GetSetting("device_labels"); err != nil || v != "" {
		t.Fatalf("after clear = %q, %v; want empty", v, err)
	}
}

type testOverride struct {
	InputPerM  float64 `json:"input_per_mtok"`
	OutputPerM float64 `json:"output_per_mtok"`
}

func TestSettingsJSONRoundTrip(t *testing.T) {
	s := openSettingsStore(t)

	in := map[string]testOverride{
		"claude-opus-4": {InputPerM: 15, OutputPerM: 75},
		"gpt-5":         {InputPerM: 1.25, OutputPerM: 10},
	}
	if err := s.SetSettingsJSON("pricing_overrides", in); err != nil {
		t.Fatal(err)
	}
	out := map[string]testOverride{}
	if err := s.GetSettingsJSON("pricing_overrides", &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["claude-opus-4"].OutputPerM != 75 || out["gpt-5"].InputPerM != 1.25 {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}

	// Replacing the document drops keys that are no longer present (the
	// settings page saves the whole table, so deletes must actually delete).
	if err := s.SetSettingsJSON("pricing_overrides", map[string]testOverride{"gpt-5": {InputPerM: 2}}); err != nil {
		t.Fatal(err)
	}
	out = map[string]testOverride{}
	if err := s.GetSettingsJSON("pricing_overrides", &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out["gpt-5"].InputPerM != 2 {
		t.Fatalf("after replace = %+v, want only gpt-5=2", out)
	}
}

func TestSettingsJSONMissingKeyKeepsDefault(t *testing.T) {
	s := openSettingsStore(t)

	cur := struct {
		Code string  `json:"code"`
		Rate float64 `json:"rate"`
	}{Code: "USD", Rate: 1}
	if err := s.GetSettingsJSON("no_such_key", &cur); err != nil {
		t.Fatalf("GetSettingsJSON on missing key: %v", err)
	}
	if cur.Code != "USD" || cur.Rate != 1 {
		t.Errorf("default clobbered: %+v", cur)
	}
}

func TestSettingsJSONCorruptIsError(t *testing.T) {
	s := openSettingsStore(t)

	if err := s.SetSetting("device_labels", "{not json"); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if err := s.GetSettingsJSON("device_labels", &out); err == nil {
		t.Error("corrupt JSON silently accepted; want error")
	}
}

func TestSettingsPersistAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("device_labels", `{"mac-mini":"书房 Mac"}`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	labels := map[string]string{}
	if err := s2.GetSettingsJSON("device_labels", &labels); err != nil {
		t.Fatal(err)
	}
	if labels["mac-mini"] != "书房 Mac" {
		t.Fatalf("labels after reopen = %+v", labels)
	}
}
