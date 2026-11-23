// Package selfupdate implements on-demand binary upgrades for Hub and Agent
// hosts (ADR-0043). It never polls in the background: upgrades happen only
// when an operator or CI workflow explicitly runs `omnitoken self-update`.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRepoURL    = "https://github.com/SuooL/OmniToken"
	maxChecksumsBytes = 64 << 10  // 64 KiB
	maxBinaryBytes    = 128 << 20 // 128 MiB
)

// CommandRunner runs a local service management command (systemctl/launchctl).
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Options configures an on-demand self-update invocation.
type Options struct {
	CurrentVersion  string
	TargetVersion   string   // optional explicit tag (e.g. "v0.5.0"); empty resolves latest
	RepoURL         string   // defaults to DefaultRepoURL
	BaseURL         string   // optional direct download base holding <asset> and SHA256SUMS
	BinaryPath      string   // defaults to os.Executable()
	CheckOnly       bool     // inspect version without downloading or modifying binary
	AllowDowngrade  bool     // allow replacing with an equal or older semver
	AutoRestart     bool     // auto-detect and restart active omnitoken services
	RestartServices []string // explicit service names to restart after replacement
	GOOS            string   // defaults to runtime.GOOS
	GOARCH          string   // defaults to runtime.GOARCH
	HTTPClient      *http.Client
	RunCommand      CommandRunner
}

// Result reports what self-update observed or changed.
type Result struct {
	CurrentVersion string
	TargetVersion  string
	Asset          string
	BinaryPath     string
	Updated        bool
	AlreadyCurrent bool
	Restarted      []string
}

// AssetName returns the GitHub Release asset name for a Go OS/arch pair.
func AssetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "omnitoken-darwin-arm64", nil
	case "darwin/amd64":
		return "omnitoken-darwin-amd64", nil
	case "linux/amd64":
		return "omnitoken-linux-amd64", nil
	case "linux/arm64":
		return "omnitoken-linux-arm64", nil
	case "windows/amd64":
		return "omnitoken-windows-amd64.exe", nil
	default:
		return "", fmt.Errorf("unsupported platform %s/%s for release binaries", goos, goarch)
	}
}

// CompareVersions compares two semver strings (with optional leading "v" and
// git-describe suffix like "-dirty" or "-3-gabc123"). Returns (-1/0/+1, true)
// when both parse as major.minor.patch, or (0, false) for non-semver values
// like "dev".
func CompareVersions(a, b string) (int, bool) {
	va, okA := parseSemver(a)
	vb, okB := parseSemver(b)
	if !okA || !okB {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if va[i] < vb[i] {
			return -1, true
		}
		if va[i] > vb[i] {
			return 1, true
		}
	}
	return 0, true
}

func parseSemver(raw string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func normalizeTag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// ParseChecksum locates asset in a SHA256SUMS manifest and returns its 64-char
// lowercase hex digest.
func ParseChecksum(manifest, asset string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(fields[0]))
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		if name != asset {
			continue
		}
		if len(digest) != 64 {
			return "", fmt.Errorf("invalid sha256 digest length for %s", asset)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("invalid sha256 hex for %s: %w", asset, err)
		}
		return digest, nil
	}
	return "", fmt.Errorf("asset %s not found in SHA256SUMS", asset)
}

// Run executes an on-demand version check or verified atomic binary upgrade.
func Run(ctx context.Context, opts Options) (Result, error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := opts.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	asset, err := AssetName(goos, goarch)
	if err != nil {
		return Result{}, err
	}
	repoURL := strings.TrimSuffix(strings.TrimSpace(opts.RepoURL), "/")
	if repoURL == "" {
		repoURL = DefaultRepoURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	runner := opts.RunCommand
	if runner == nil {
		runner = defaultRunner
	}

	targetVer := normalizeTag(opts.TargetVersion)
	baseURL := strings.TrimSuffix(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		if targetVer == "" {
			resolved, err := resolveLatestTag(ctx, client, repoURL)
			if err != nil {
				return Result{}, err
			}
			targetVer = resolved
		}
		baseURL = repoURL + "/releases/download/" + targetVer
	}

	binPath := opts.BinaryPath
	if binPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("locate current executable: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			binPath = resolved
		} else {
			binPath = exe
		}
	}

	res := Result{
		CurrentVersion: opts.CurrentVersion,
		TargetVersion:  targetVer,
		Asset:          asset,
		BinaryPath:     binPath,
	}

	if targetVer != "" && !opts.AllowDowngrade {
		if cmp, ok := CompareVersions(opts.CurrentVersion, targetVer); ok && cmp >= 0 {
			res.AlreadyCurrent = true
			return res, nil
		}
	}
	if opts.CheckOnly {
		return res, nil
	}

	manifest, err := fetchText(ctx, client, baseURL+"/SHA256SUMS", maxChecksumsBytes)
	if err != nil {
		return res, fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	wantHash, err := ParseChecksum(manifest, asset)
	if err != nil {
		return res, err
	}

	tmpPath, err := downloadVerified(ctx, client, baseURL+"/"+asset, filepath.Dir(binPath), wantHash)
	if err != nil {
		return res, err
	}
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := replaceBinary(tmpPath, binPath, goos); err != nil {
		return res, err
	}
	tmpPath = "" // ownership moved to binPath
	res.Updated = true

	if opts.AutoRestart || len(opts.RestartServices) > 0 {
		restarted, err := restartServices(ctx, goos, opts.AutoRestart, opts.RestartServices, runner)
		res.Restarted = restarted
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func resolveLatestTag(ctx context.Context, baseClient *http.Client, repoURL string) (string, error) {
	noRedirect := *baseClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("resolve latest release: unexpected HTTP %d", resp.StatusCode)
	}
	loc := strings.TrimSpace(resp.Header.Get("Location"))
	const marker = "/releases/tag/"
	i := strings.Index(loc, marker)
	if i < 0 {
		return "", fmt.Errorf("resolve latest release: redirect %q has no tag", loc)
	}
	tag := strings.TrimSpace(loc[i+len(marker):])
	if q := strings.IndexAny(tag, "?#/"); q >= 0 {
		tag = tag[:q]
	}
	if tag == "" {
		return "", errors.New("resolve latest release: empty tag")
	}
	return tag, nil
}

func fetchText(ctx context.Context, client *http.Client, url string, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("response from %s exceeds %d bytes", url, maxBytes)
	}
	return string(data), nil
}

func downloadVerified(ctx context.Context, client *http.Client, url, dir, wantHex string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download binary: HTTP %d from %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(dir, ".omnitoken-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxBinaryBytes+1))
	if err != nil {
		cleanup()
		return "", fmt.Errorf("write temp binary: %w", err)
	}
	if n > maxBinaryBytes {
		cleanup()
		return "", fmt.Errorf("binary exceeds maximum size %d bytes", maxBinaryBytes)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("sync temp binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	gotHex := hex.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(gotHex), []byte(wantHex)) != 1 {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", gotHex, wantHex)
	}
	return tmpPath, nil
}

func replaceBinary(tmpPath, targetPath, goos string) error {
	mode := os.FileMode(0o755)
	if info, err := os.Stat(targetPath); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Chmod(tmpPath, mode); err != nil && goos != "windows" {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if goos == "windows" {
		oldPath := targetPath + ".old"
		_ = os.Remove(oldPath)
		if _, err := os.Stat(targetPath); err == nil {
			if err := os.Rename(targetPath, oldPath); err != nil {
				return fmt.Errorf("stage existing windows binary: %w", err)
			}
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			_ = os.Rename(oldPath, targetPath)
			return fmt.Errorf("install new windows binary: %w", err)
		}
		return nil
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("replace binary %s: %w", targetPath, err)
	}
	return nil
}

func restartServices(ctx context.Context, goos string, auto bool, explicit []string, run CommandRunner) ([]string, error) {
	var targets []string
	for _, s := range explicit {
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				targets = append(targets, p)
			}
		}
	}

	switch goos {
	case "linux":
		if len(targets) == 0 && auto {
			for _, candidate := range []string{"omnitoken-server", "omnitoken-agent"} {
				if _, err := run(ctx, "systemctl", "is-active", "--quiet", candidate); err == nil {
					targets = append(targets, candidate)
				} else if _, err := run(ctx, "systemctl", "--user", "is-active", "--quiet", candidate); err == nil {
					targets = append(targets, "user:"+candidate)
				}
			}
		}
		var restarted []string
		for _, svc := range targets {
			if strings.HasPrefix(svc, "user:") {
				name := strings.TrimPrefix(svc, "user:")
				if out, err := run(ctx, "systemctl", "--user", "restart", name); err != nil {
					return restarted, fmt.Errorf("restart user service %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
				}
				restarted = append(restarted, name)
				continue
			}
			if out, err := run(ctx, "systemctl", "restart", svc); err != nil {
				return restarted, fmt.Errorf("restart service %s: %w (%s)", svc, err, strings.TrimSpace(string(out)))
			}
			restarted = append(restarted, svc)
		}
		return restarted, nil

	case "darwin":
		if len(targets) == 0 && auto {
			targets = []string{"com.omnitoken.agent"}
		}
		uid := strconv.Itoa(os.Getuid())
		var restarted []string
		for _, label := range targets {
			target := "gui/" + uid + "/" + label
			if out, err := run(ctx, "launchctl", "kickstart", "-k", target); err != nil {
				return restarted, fmt.Errorf("restart launchd %s: %w (%s)", label, err, strings.TrimSpace(string(out)))
			}
			restarted = append(restarted, label)
		}
		return restarted, nil

	default:
		if len(targets) > 0 {
			return nil, fmt.Errorf("service restart is not implemented on %s; restart the service manually", goos)
		}
		return nil, nil
	}
}

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
