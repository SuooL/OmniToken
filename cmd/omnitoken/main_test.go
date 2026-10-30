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
	"github.com/suool/omnitoken/internal/siteauth"
)

func TestAuthCodeCommandUsesSeparateDatabase(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"db": filepath.Join(dir, "usage.db"), "site_auth_origin": "https://omni.example.test"})
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runAuthCodeWith([]string{"-config", config, "-ttl", "15m"}, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "usage.db")); !os.IsNotExist(err) {
		t.Fatal("code command touched usage database")
	}
	a, err := siteauth.Open(filepath.Join(dir, "site-auth.db"), "https://omni.example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(output.String())})
	r := httptest.NewRequest("POST", "https://omni.example.test/auth/code", bytes.NewReader(body))
	r.Header.Set("Origin", "https://omni.example.test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("generated code rejected: %d", w.Code)
	}
	if err := runAuthCodeWith([]string{"-config", config, "-ttl", "2h"}, &output); err == nil {
		t.Fatal("overlong lifetime accepted")
	}
}

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
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": request["device_id"]})
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
