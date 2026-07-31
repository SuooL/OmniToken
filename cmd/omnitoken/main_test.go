package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suool/omnitoken/internal/agent"
)

func TestAgentEnrollPersistsStableIdentityAndDoesNotPrintCredentials(t *testing.T) {
	var requests []map[string]any
	var authorization []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = append(authorization, r.Header.Get("Authorization"))
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"device_id":"ok"}`))
	}))
	defer server.Close()
	t.Setenv("OMNITOKEN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("OMNITOKEN_DEVICE_TOKEN", "device-secret")
	configPath := filepath.Join(t.TempDir(), "agent.json")

	var output bytes.Buffer
	if err := runAgentEnrollWith([]string{
		"-config", configPath,
		"-server", server.URL,
		"-name", "Before",
		"-allow-insecure-http",
	}, &output); err != nil {
		t.Fatal(err)
	}
	first, err := agent.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProtocolVersion != 2 || first.DeviceID == "" || first.DeviceToken != "device-secret" ||
		!first.AllowInsecureHTTP {
		t.Fatalf("first config = %+v", first)
	}
	if strings.Contains(output.String(), "device-secret") || strings.Contains(output.String(), "admin-secret") {
		t.Fatalf("enrollment output leaked credential: %q", output.String())
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%#o, want 0600", info.Mode().Perm())
	}

	output.Reset()
	if err := runAgentEnrollWith([]string{
		"-config", configPath,
		"-server", server.URL,
		"-name", "After",
	}, &output); err != nil {
		t.Fatal(err)
	}
	second, err := agent.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.DeviceID != first.DeviceID || second.DeviceToken != first.DeviceToken || second.Name != "After" {
		t.Fatalf("rename changed identity: before=%+v after=%+v", first, second)
	}
	if len(requests) != 2 || requests[0]["device_id"] != requests[1]["device_id"] ||
		requests[0]["device_token"] != requests[1]["device_token"] {
		t.Fatalf("enrollment requests changed identity: %#v", requests)
	}
	for _, got := range authorization {
		if got != "Bearer admin-secret" {
			t.Fatalf("authorization=%q", got)
		}
	}
}

func TestAgentEnrollDoesNotPersistCredentialsWhenHubRejects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("OMNITOKEN_ADMIN_TOKEN", "wrong")
	configPath := filepath.Join(t.TempDir(), "agent.json")

	if err := runAgentEnrollWith([]string{
		"-config", configPath,
		"-server", server.URL,
		"-name", "Agent",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("rejected enrollment returned success")
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("rejected enrollment persisted config: %v", err)
	}
}

func TestAgentEnrollRejectsSecretCommandLineFlags(t *testing.T) {
	for _, flagName := range []string{"-admin-token", "-device-token"} {
		err := runAgentEnrollWith([]string{flagName, "secret"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("%s error=%v, want undefined flag so credentials cannot enter argv", flagName, err)
		}
	}
}
