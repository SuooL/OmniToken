package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/selfupdate"
)

const (
	autoUpdateDownloadTimeout = 5 * time.Minute
	autoUpdateShutdownTimeout = 5 * time.Second
	autoUpdateJitterBase      = 5 * time.Second
	autoUpdateJitterSpan      = 20 * time.Second
	maxUpdateWarningLen       = 200
)

type heartbeatResponse struct {
	HubVersion   string `json:"hub_version"`
	ReleaseProxy bool   `json:"release_proxy"`
}

type releaseAuthTransport struct {
	base        http.RoundTripper
	bearerToken string
	deviceID    string
	relayToken  string
}

func (t *releaseAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.bearerToken != "" {
		clone.Header.Set("Authorization", "Bearer "+t.bearerToken)
	}
	if t.deviceID != "" {
		clone.Header.Set(deviceIDHeader, t.deviceID)
	}
	if t.relayToken != "" {
		clone.Header.Set(relayTokenHeader, t.relayToken)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// NewReleaseHTTPClient builds an authenticated HTTP client for downloading
// release assets from the Hub release proxy configured in fc (including
// resolve_ip, device credentials, and relay token when configured).
func NewReleaseHTTPClient(fc FileConfig) (*http.Client, string, error) {
	hubURL, err := validateServerURLWithPolicy(fc.Server, fc.AllowInsecureHTTP, fc.SubnetOnly)
	if err != nil {
		return nil, "", err
	}
	if fc.SubnetOnly && fc.ResolveIP != "" && !subnetHost(fc.ResolveIP) {
		return nil, "", fmt.Errorf("subnet_only rejects public resolve_ip %q", fc.ResolveIP)
	}
	baseClient, err := newHTTPClient(autoUpdateDownloadTimeout, hubURL.String(), fc.ResolveIP)
	if err != nil {
		return nil, "", err
	}
	bearer := fc.Token
	deviceID := ""
	if fc.EffectiveProtocolVersion() == 2 && fc.DeviceID != "" && fc.DeviceToken != "" {
		bearer = fc.DeviceToken
		deviceID = fc.DeviceID
	}
	relayToken := fc.RelayUpstreamToken
	if relayToken == "" {
		relayToken = fc.RelayToken
	}
	baseClient.Transport = &releaseAuthTransport{
		base:        baseClient.Transport,
		bearerToken: bearer,
		deviceID:    deviceID,
		relayToken:  relayToken,
	}
	return baseClient, hubURL.String(), nil
}

// ResolveHubVersion queries <serverURL>/api/v1/health for the Hub's running
// release tag. Relays return {"status":"relay"} without a hub_version, in
// which case callers must specify -version explicitly.
func ResolveHubVersion(ctx context.Context, client *http.Client, serverURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/api/v1/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("query hub health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query hub health: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		HubVersion string `json:"hub_version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode hub health: %w", err)
	}
	ver := strings.TrimSpace(payload.HubVersion)
	if ver == "" || ver == "dev" {
		return "", errors.New("hub health did not report a release version (specify -version vX.Y.Z when downloading via a relay)")
	}
	return ver, nil
}

func (a *Agent) effectiveAutoUpdateSource() string {
	source, err := normalizeAutoUpdateSource(a.cfg.AutoUpdateSource)
	if err != nil || source == "" {
		return AutoUpdateSourceHub
	}
	return source
}

func (a *Agent) currentWarnings() []string {
	a.updateMu.Lock()
	warn := a.updateWarning
	a.updateMu.Unlock()
	if warn == "" {
		return nil
	}
	return []string{warn}
}

func (a *Agent) maybeAutoUpdate(hubVersion string, releaseProxy bool) {
	if !a.cfg.AutoUpdate || !a.isV2() {
		return
	}
	target := strings.TrimSpace(hubVersion)
	current := strings.TrimSpace(a.cfg.AgentVersion)
	cmp, ok := selfupdate.CompareVersions(current, target)
	if !ok || cmp >= 0 {
		return
	}
	source := a.effectiveAutoUpdateSource()
	if source == AutoUpdateSourceHub && !releaseProxy {
		return
	}
	if a.cfg.SubnetOnly && source == AutoUpdateSourceGitHub {
		return
	}

	now := a.currentTime()
	a.updateMu.Lock()
	if a.updating {
		a.updateMu.Unlock()
		return
	}
	if a.updateFailedVersion == target && now.Before(a.updateBackoffUntil) {
		a.updateMu.Unlock()
		return
	}
	a.updating = true
	a.updateMu.Unlock()

	if a.updateSync {
		a.performAutoUpdate(target, source)
		return
	}
	go a.performAutoUpdate(target, source)
}

func (a *Agent) performAutoUpdate(target, source string) {
	defer func() {
		a.updateMu.Lock()
		a.updating = false
		a.updateMu.Unlock()
	}()

	if delay := a.nextUpdateJitter(); delay > 0 {
		if a.sleep != nil {
			a.sleep(delay)
		} else {
			time.Sleep(delay)
		}
	}

	opts := selfupdate.Options{
		CurrentVersion: a.cfg.AgentVersion,
		TargetVersion:  target,
		AutoRestart:    false,
	}
	if source == AutoUpdateSourceHub {
		var baseTransport http.RoundTripper
		if a.client != nil {
			baseTransport = a.client.Transport
		}
		opts.BaseURL = strings.TrimRight(a.cfg.ServerURL, "/") + "/api/v2/releases/" + target
		opts.HTTPClient = &http.Client{
			Timeout: autoUpdateDownloadTimeout,
			Transport: &releaseAuthTransport{
				base:        baseTransport,
				bearerToken: a.cfg.DeviceToken,
				deviceID:    a.cfg.DeviceID,
				relayToken:  a.relayUpstreamToken(),
			},
		}
	} else {
		opts.HTTPClient = &http.Client{Timeout: autoUpdateDownloadTimeout}
	}

	run := a.runSelfUpdate
	if run == nil {
		run = selfupdate.Run
	}
	ctx, cancel := context.WithTimeout(context.Background(), autoUpdateDownloadTimeout)
	defer cancel()

	res, err := run(ctx, opts)
	if err != nil {
		delay := a.recordUpdateFailure(target, err)
		log.Printf("agent: auto-update %s -> %s (%s) failed (retry in %s): %v",
			a.cfg.AgentVersion, target, source, delay, err)
		return
	}
	a.clearUpdateFailure()
	if !res.Updated {
		return
	}

	log.Printf("agent: auto-updated %s -> %s via %s (%s); restarting",
		res.CurrentVersion, res.TargetVersion, source, res.BinaryPath)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), autoUpdateShutdownTimeout)
	a.shutdownRelay(shutdownCtx)
	shutdownCancel()
	_ = a.Close()

	reexec := a.reexecSelf
	if reexec == nil {
		reexec = reexecSelfProcess
	}
	if err := reexec(res.BinaryPath); err != nil {
		log.Printf("agent: restart after auto-update failed (%v); exiting for supervisor restart", err)
		os.Exit(75)
	}
}

func (a *Agent) nextUpdateJitter() time.Duration {
	if a.updateJitterDelay != nil {
		return a.updateJitterDelay()
	}
	if a.jitter == nil {
		return autoUpdateJitterBase
	}
	return autoUpdateJitterBase + time.Duration(a.jitter()*float64(autoUpdateJitterSpan))
}

func (a *Agent) recordUpdateFailure(target string, err error) time.Duration {
	now := a.currentTime()
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if a.updateFailedVersion != target {
		a.updateFailedVersion = target
		a.updateAttempts = 0
	}
	a.updateAttempts++
	var delay time.Duration
	switch {
	case a.updateAttempts <= 1:
		delay = 5 * time.Minute
	case a.updateAttempts == 2:
		delay = 15 * time.Minute
	default:
		delay = time.Hour
	}
	a.updateBackoffUntil = now.Add(delay)
	warn := fmt.Sprintf("auto_update %s: %v", target, err)
	if len(warn) > maxUpdateWarningLen {
		warn = warn[:maxUpdateWarningLen-3] + "..."
	}
	a.updateWarning = warn
	return delay
}

func (a *Agent) clearUpdateFailure() {
	a.updateMu.Lock()
	a.updateAttempts = 0
	a.updateFailedVersion = ""
	a.updateBackoffUntil = time.Time{}
	a.updateWarning = ""
	a.updateMu.Unlock()
}

// cleanupStaleOldBinaries removes leftover "<binary>.old.<pid>" staging files
// created during Windows self-updates after the new binary starts up.
func cleanupStaleOldBinaries() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cleanupStaleOldBinariesInDir(filepath.Dir(exe), filepath.Base(exe))
}

func cleanupStaleOldBinariesInDir(dir, baseName string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := baseName + ".old."
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := name[len(prefix):]
		if suffix == "" || !allDigits(suffix) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
