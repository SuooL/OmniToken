package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"darwin", "arm64", "omnitoken-darwin-arm64", false},
		{"darwin", "amd64", "omnitoken-darwin-amd64", false},
		{"linux", "amd64", "omnitoken-linux-amd64", false},
		{"linux", "arm64", "omnitoken-linux-arm64", false},
		{"windows", "amd64", "omnitoken-windows-amd64.exe", false},
		{"windows", "arm64", "", true},
		{"freebsd", "amd64", "", true},
	}
	for _, tc := range cases {
		got, err := AssetName(tc.goos, tc.goarch)
		if (err != nil) != tc.wantErr {
			t.Errorf("AssetName(%s,%s) err=%v wantErr=%v", tc.goos, tc.goarch, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("AssetName(%s,%s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.4.2", "v0.5.0", -1, true},
		{"0.5.0", "v0.5.0", 0, true},
		{"v1.0.0", "v0.9.9", 1, true},
		{"v0.4.2-dirty", "v0.4.2", 0, true},
		{"v0.4.2-3-g180c629", "v0.4.3", -1, true},
		{"dev", "v0.5.0", 0, false},
		{"180c629", "v0.5.0", 0, false},
	}
	for _, tc := range cases {
		got, ok := CompareVersions(tc.a, tc.b)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("CompareVersions(%q,%q) = (%d,%v), want (%d,%v)", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sums := digest + "  omnitoken-linux-amd64\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *omnitoken-darwin-arm64\n"
	got, err := ParseChecksum(sums, "omnitoken-linux-amd64")
	if err != nil || got != digest {
		t.Fatalf("ParseChecksum linux = %q, %v", got, err)
	}
	got2, err := ParseChecksum(sums, "omnitoken-darwin-arm64")
	if err != nil || got2 != strings.Repeat("b", 64) {
		t.Fatalf("ParseChecksum darwin = %q, %v", got2, err)
	}
	if _, err := ParseChecksum(sums, "omnitoken-windows-amd64.exe"); err == nil {
		t.Fatal("expected error for missing asset")
	}
}

func TestRunCheckAndReplaceWithRedirect(t *testing.T) {
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test host %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	newBin := []byte("#!/bin/sh\necho new-binary-v0.5.0\n")
	sum := sha256.Sum256(newBin)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v0.5.0", http.StatusFound)
		case "/releases/download/v0.5.0/SHA256SUMS":
			w.Write([]byte(sums))
		case "/releases/download/v0.5.0/" + asset:
			w.Write(newBin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Check-only mode does not touch disk.
	dir := t.TempDir()
	target := filepath.Join(dir, "omnitoken")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{
		CurrentVersion: "v0.4.2",
		RepoURL:        srv.URL,
		BinaryPath:     target,
		CheckOnly:      true,
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("check only: %v", err)
	}
	if res.TargetVersion != "v0.5.0" || res.Updated || res.AlreadyCurrent {
		t.Fatalf("unexpected check result: %+v", res)
	}
	if data, _ := os.ReadFile(target); string(data) != "old-binary" {
		t.Fatalf("check only mutated binary: %q", string(data))
	}

	// Already up-to-date skips download.
	resUp, err := Run(context.Background(), Options{
		CurrentVersion: "v0.5.0",
		RepoURL:        srv.URL,
		BinaryPath:     target,
		HTTPClient:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("up-to-date run: %v", err)
	}
	if !resUp.AlreadyCurrent || resUp.Updated {
		t.Fatalf("expected AlreadyCurrent, got %+v", resUp)
	}

	// Actual upgrade replaces atomically and preserves executable mode.
	var restarted []string
	resDo, err := Run(context.Background(), Options{
		CurrentVersion:  "v0.4.2",
		RepoURL:         srv.URL,
		BinaryPath:      target,
		RestartServices: []string{"omnitoken-server"},
		HTTPClient:      srv.Client(),
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			restarted = append(restarted, name+" "+strings.Join(args, " "))
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("upgrade run: %v", err)
	}
	if !resDo.Updated || resDo.TargetVersion != "v0.5.0" {
		t.Fatalf("unexpected upgrade result: %+v", resDo)
	}
	gotData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, newBin) {
		t.Fatalf("replaced binary mismatch: got %q", string(gotData))
	}
	if len(restarted) == 0 {
		t.Fatal("expected restart command to be invoked")
	}
}

func TestRunRejectsChecksumMismatchWithoutTouchingBinary(t *testing.T) {
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test host %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), asset)
		case "/" + asset:
			w.Write([]byte("tampered-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "omnitoken")
	if err := os.WriteFile(target, []byte("original-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), Options{
		CurrentVersion: "v0.4.2",
		TargetVersion:  "v0.5.0",
		BaseURL:        srv.URL,
		BinaryPath:     target,
		HTTPClient:     srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original-good" {
		t.Fatalf("target was modified after checksum failure: %q", string(got))
	}
	// Ensure temp file was cleaned up.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files in dir: %v", entries)
	}
}

func TestRestartDetectionOnLinuxAndDarwin(t *testing.T) {
	var linuxCalls []string
	restarted, err := restartServices(context.Background(), "linux", true, nil, func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		linuxCalls = append(linuxCalls, cmd)
		if name == "systemctl" && len(args) >= 3 && args[0] == "is-active" && args[2] == "omnitoken-server" {
			return []byte("active\n"), nil
		}
		if name == "systemctl" && strings.Contains(cmd, "is-active") {
			return nil, fmt.Errorf("inactive")
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("linux restart: %v", err)
	}
	if len(restarted) != 1 || restarted[0] != "omnitoken-server" {
		t.Fatalf("linux restarted = %v, calls = %v", restarted, linuxCalls)
	}

	// User-scoped systemd unit on Linux.
	restartedUser, err := restartServices(context.Background(), "linux", true, nil, func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		if cmd == "systemctl --user is-active --quiet omnitoken-agent" {
			return []byte("active\n"), nil
		}
		if strings.Contains(cmd, "is-active") {
			return nil, fmt.Errorf("inactive")
		}
		return nil, nil
	})
	if err != nil || len(restartedUser) != 1 || restartedUser[0] != "omnitoken-agent" {
		t.Fatalf("linux user restart = %v, err=%v", restartedUser, err)
	}

	var macCalls []string
	restartedMac, err := restartServices(context.Background(), "darwin", true, nil, func(_ context.Context, name string, args ...string) ([]byte, error) {
		macCalls = append(macCalls, name+" "+strings.Join(args, " "))
		return nil, nil
	})
	if err != nil {
		t.Fatalf("darwin restart: %v", err)
	}
	if len(restartedMac) != 1 || restartedMac[0] != "com.omnitoken.agent" {
		t.Fatalf("darwin restarted = %v, calls = %v", restartedMac, macCalls)
	}
}
