package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/suool/omnitoken/internal/selfupdate"
)

const (
	deviceIDHeader           = "X-OmniToken-Device-ID"
	releaseChecksumAsset     = "SHA256SUMS"
	releaseProxyMaxChecksum  = 64 << 10  // 64 KiB
	releaseProxyMaxBinary    = 128 << 20 // 128 MiB
	releaseProxyFetchTimeout = 3 * time.Minute
	releaseProxyWriteTimeout = 5 * time.Minute
	releaseCacheMaxVersions  = 3
)

var allowedReleaseAssets = map[string]struct{}{
	releaseChecksumAsset:          {},
	"omnitoken-darwin-arm64":      {},
	"omnitoken-darwin-amd64":      {},
	"omnitoken-linux-amd64":       {},
	"omnitoken-linux-arm64":       {},
	"omnitoken-windows-amd64.exe": {},
}

func (s *Server) releaseProxyEnabled() bool {
	return s.cfg == nil || !s.cfg.ReleaseProxyDisabled
}

func (s *Server) releaseCacheRoot() string {
	dbPath := ""
	if s.cfg != nil {
		dbPath = s.cfg.DBPath
	}
	if dbPath == "" {
		return filepath.Join(DataDir(), "release-cache")
	}
	return filepath.Join(filepath.Dir(dbPath), "release-cache")
}

func (s *Server) releaseUpstreamBase() string {
	if s.cfg != nil {
		if mirror := strings.TrimRight(strings.TrimSpace(s.cfg.ReleaseMirrorURL), "/"); mirror != "" {
			return mirror
		}
	}
	return selfupdate.DefaultRepoURL
}

func (s *Server) releaseClient() *http.Client {
	if s.releaseHTTPClient != nil {
		return s.releaseHTTPClient
	}
	return &http.Client{Timeout: releaseProxyFetchTimeout}
}

func (s *Server) releaseFetchLock(key string) *sync.Mutex {
	s.releaseProxyMu.Lock()
	defer s.releaseProxyMu.Unlock()
	if s.releaseFetchLocks == nil {
		s.releaseFetchLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := s.releaseFetchLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		s.releaseFetchLocks[key] = mu
	}
	return mu
}

// handleReleaseAssetV2 serves authenticated, SHA-256-verified, disk-cached
// release assets so intranet or GitHub-blocked agents can self-update strictly
// over the Hub/relay subnet (ADR-0046).
func (s *Server) handleReleaseAssetV2(w http.ResponseWriter, r *http.Request) {
	if !s.releaseProxyEnabled() {
		writeIngestV2Error(w, http.StatusNotFound, "release_proxy_disabled")
		return
	}
	status, code := s.authorizeReleaseProxy(r)
	if status != 0 {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="omnitoken-device"`)
		}
		writeIngestV2Error(w, status, code)
		return
	}

	version := r.PathValue("version")
	asset := r.PathValue("asset")
	if !validReleaseVersion(version) || !allowedReleaseAsset(asset) {
		writeIngestV2Error(w, http.StatusBadRequest, "invalid_release_request")
		return
	}

	cachePath, errStatus, err := s.ensureReleaseAsset(r.Context(), version, asset)
	if err != nil {
		log.Printf("release-proxy: %s/%s: %v", version, asset, err)
		if errStatus == http.StatusNotFound {
			writeIngestV2Error(w, http.StatusNotFound, "release_not_found")
			return
		}
		writeIngestV2Error(w, http.StatusBadGateway, "release_fetch_failed")
		return
	}

	// Extending the response write deadline per-request allows ~16 MiB binaries
	// to stream cleanly across LAN/mesh relays without loosening WriteTimeout
	// for normal JSON endpoints.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(releaseProxyWriteTimeout))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	http.ServeFile(w, r, cachePath)
}

func (s *Server) authorizeReleaseProxy(r *http.Request) (int, string) {
	// If X-OmniToken-Device-ID is provided, strictly authenticate as that v2
	// device principal so revoked or forged device tokens can never fall back
	// to another credential path.
	if deviceID := strings.TrimSpace(r.Header.Get(deviceIDHeader)); deviceID != "" {
		if !validCanonicalUUID(deviceID) {
			return http.StatusUnauthorized, "unauthorized"
		}
		cred, ok := bearerCredential(r)
		if !ok || s.store == nil {
			return http.StatusUnauthorized, "unauthorized"
		}
		_, authenticated, err := s.store.AuthenticateDevice(deviceID, cred)
		if err != nil {
			return http.StatusInternalServerError, "authentication_error"
		}
		if !authenticated {
			return http.StatusUnauthorized, "unauthorized"
		}
		return 0, ""
	}

	if s.siteAuth != nil && s.siteAuth.Authenticated(r) {
		return 0, ""
	}
	if s.cfg != nil {
		if s.cfg.ReadToken != "" && credentialOK(r, s.cfg.ReadToken) {
			return 0, ""
		}
		if s.cfg.Token != "" && credentialOK(r, s.cfg.Token) {
			return 0, ""
		}
		if s.cfg.AdminToken != "" && credentialOK(r, s.cfg.AdminToken) {
			return 0, ""
		}
		if s.siteAuth == nil && s.loopbackOnly() && s.cfg.ReadToken == "" && s.cfg.Token == "" && s.cfg.AdminToken == "" {
			return 0, ""
		}
	}
	return http.StatusUnauthorized, "unauthorized"
}

func (s *Server) ensureReleaseAsset(ctx context.Context, version, asset string) (string, int, error) {
	cacheRoot := s.releaseCacheRoot()
	versionDir := filepath.Join(cacheRoot, version)
	cachePath := filepath.Join(versionDir, asset)

	if regularFileExists(cachePath) {
		return cachePath, 0, nil
	}

	// Ensure SHA256SUMS is cached before fetching any binary so we can verify
	// upstream bytes before they ever enter the Hub disk cache.
	var wantHex string
	if asset != releaseChecksumAsset {
		manifestPath, status, err := s.ensureReleaseAsset(ctx, version, releaseChecksumAsset)
		if err != nil {
			return "", status, fmt.Errorf("ensure %s: %w", releaseChecksumAsset, err)
		}
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			return "", http.StatusBadGateway, fmt.Errorf("read cached %s: %w", releaseChecksumAsset, err)
		}
		wantHex, err = selfupdate.ParseChecksum(string(manifest), asset)
		if err != nil {
			return "", http.StatusBadGateway, err
		}
	}

	mu := s.releaseFetchLock(version + "/" + asset)
	mu.Lock()
	defer mu.Unlock()

	if regularFileExists(cachePath) {
		return cachePath, 0, nil
	}
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("create release cache dir: %w", err)
	}

	upstreamURL := s.releaseUpstreamBase() + "/releases/download/" + version + "/" + asset
	fetchCtx, cancel := context.WithTimeout(ctx, releaseProxyFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	resp, err := s.releaseClient().Do(req)
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("fetch upstream %s: %w", upstreamURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", http.StatusNotFound, fmt.Errorf("upstream HTTP 404 for %s", upstreamURL)
	}
	if resp.StatusCode != http.StatusOK {
		return "", http.StatusBadGateway, fmt.Errorf("upstream HTTP %d for %s", resp.StatusCode, upstreamURL)
	}

	maxBytes := int64(releaseProxyMaxBinary)
	if asset == releaseChecksumAsset {
		maxBytes = releaseProxyMaxChecksum
	}

	tmp, err := os.CreateTemp(versionDir, ".fetch-"+asset+"-*")
	if err != nil {
		return "", http.StatusBadGateway, fmt.Errorf("create cache temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		cleanup()
		return "", http.StatusBadGateway, fmt.Errorf("stream upstream %s: %w", asset, err)
	}
	if n == 0 {
		cleanup()
		return "", http.StatusBadGateway, fmt.Errorf("upstream %s returned empty body", asset)
	}
	if n > maxBytes {
		cleanup()
		return "", http.StatusBadGateway, fmt.Errorf("upstream %s exceeds %d bytes", asset, maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", http.StatusBadGateway, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", http.StatusBadGateway, fmt.Errorf("close temp file: %w", err)
	}

	if asset == releaseChecksumAsset {
		raw, err := os.ReadFile(tmpPath)
		if err != nil || !validChecksumsManifest(string(raw)) {
			_ = os.Remove(tmpPath)
			return "", http.StatusBadGateway, errors.New("upstream SHA256SUMS manifest is invalid")
		}
	} else {
		gotHex := hex.EncodeToString(hasher.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(gotHex), []byte(wantHex)) != 1 {
			_ = os.Remove(tmpPath)
			return "", http.StatusBadGateway, fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, gotHex, wantHex)
		}
	}

	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return "", http.StatusBadGateway, fmt.Errorf("chmod cache file: %w", err)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		_ = os.Remove(tmpPath)
		return "", http.StatusBadGateway, fmt.Errorf("commit cache file: %w", err)
	}

	pruneReleaseCache(cacheRoot, version, releaseCacheMaxVersions)
	return cachePath, 0, nil
}

func validReleaseVersion(v string) bool {
	if !strings.HasPrefix(v, "v") || len(v) > 32 {
		return false
	}
	parts := strings.Split(v[1:], ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 8 {
			return false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	return true
}

func allowedReleaseAsset(asset string) bool {
	_, ok := allowedReleaseAssets[asset]
	return ok
}

func validChecksumsManifest(raw string) bool {
	for asset := range allowedReleaseAssets {
		if asset == releaseChecksumAsset {
			continue
		}
		if _, err := selfupdate.ParseChecksum(raw, asset); err == nil {
			return true
		}
	}
	return false
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func pruneReleaseCache(cacheRoot, keepVersion string, maxVersions int) {
	if maxVersions <= 0 {
		return
	}
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && validReleaseVersion(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) <= maxVersions {
		return
	}
	sort.Slice(versions, func(i, j int) bool {
		cmp, ok := selfupdate.CompareVersions(versions[i], versions[j])
		if ok && cmp != 0 {
			return cmp > 0 // newest first
		}
		return versions[i] > versions[j]
	})
	keep := map[string]struct{}{}
	if validReleaseVersion(keepVersion) {
		keep[keepVersion] = struct{}{}
	}
	for _, ver := range versions {
		if len(keep) >= maxVersions {
			break
		}
		keep[ver] = struct{}{}
	}
	for _, ver := range versions {
		if _, ok := keep[ver]; !ok {
			_ = os.RemoveAll(filepath.Join(cacheRoot, ver))
		}
	}
}
