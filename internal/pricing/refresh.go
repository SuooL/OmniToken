package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CatalogURL is fixed: no credentials or user data are sent upstream.
const CatalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
const catalogAPIURL = "https://api.github.com/repos/BerriAI/litellm/contents/model_prices_and_context_window.json"
const catalogMaxBytes = 16 << 20
const minimumCatalogModels = 100

var costFields = []string{"input_cost_per_token", "output_cost_per_token", "cache_read_input_token_cost", "cache_creation_input_token_cost", "cache_creation_input_token_cost_above_1hr"}

// Snapshot contains only token prices; event identities and counts never change.
type Snapshot struct {
	FetchedAt time.Time        `json:"fetched_at"`
	Models    map[string]Price `json:"models"`
}

// ParseCatalog discards unrelated LiteLLM metadata, but rejects a candidate if
// any retained price is malformed. Missing/null fields are unpriced, not zero.
func ParseCatalog(raw []byte) (map[string]Price, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("catalog JSON: %w", err)
	}
	models := map[string]Price{}
	for name, entry := range entries {
		if name == "sample_spec" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			continue
		}
		rates := map[string]float64{}
		for _, key := range costFields {
			value, present := fields[key]
			if !present || string(value) == "null" {
				continue
			}
			var rate float64
			if err := json.Unmarshal(value, &rate); err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
				return nil, fmt.Errorf("invalid %s for model %q", key, name)
			}
			rates[key] = rate
		}
		if len(rates) == 0 {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "" {
			return nil, fmt.Errorf("empty model ID")
		}
		if _, exists := models[normalized]; exists {
			return nil, fmt.Errorf("duplicate normalized model %q", normalized)
		}
		models[normalized] = Price{Input: rates[costFields[0]], Output: rates[costFields[1]], CacheRead: rates[costFields[2]], CacheWrite: rates[costFields[3]], CacheWrite1h: rates[costFields[4]]}
	}
	if err := validateCatalog(models); err != nil {
		return nil, err
	}
	return models, nil
}

func validateCatalog(models map[string]Price) error {
	if len(models) < minimumCatalogModels {
		return fmt.Errorf("catalog has %d models; need at least %d", len(models), minimumCatalogModels)
	}
	for name, p := range models {
		if name == "" || name != strings.ToLower(strings.TrimSpace(name)) {
			return fmt.Errorf("invalid normalized model ID %q", name)
		}
		for _, rate := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h} {
			if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
				return fmt.Errorf("invalid price for model %q", name)
			}
		}
	}
	return nil
}

func FetchCatalog(ctx context.Context, client *http.Client) (map[string]Price, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lastErr error
	for _, url := range []string{CatalogURL, catalogAPIURL} {
		attempt, cancelAttempt := context.WithTimeout(ctx, 14*time.Second)
		models, err := fetchCatalogURL(attempt, client, url)
		cancelAttempt()
		if err == nil {
			return models, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func fetchCatalogURL(ctx context.Context, client *http.Client, url string) (map[string]Price, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("User-Agent", "OmniToken-pricing-refresh")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > catalogMaxBytes {
		return nil, fmt.Errorf("catalog exceeds %d bytes", catalogMaxBytes)
	}
	return ParseCatalog(raw)
}

func ReadSnapshot(path string) (Snapshot, error) {
	var snapshot Snapshot
	f, err := os.Open(path)
	if err != nil {
		return snapshot, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, catalogMaxBytes+1))
	if err != nil {
		return snapshot, err
	}
	if len(raw) > catalogMaxBytes {
		return snapshot, fmt.Errorf("pricing cache too large")
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, validateSnapshot(snapshot)
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.FetchedAt.IsZero() || snapshot.FetchedAt.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("invalid pricing snapshot timestamp")
	}
	return validateCatalog(snapshot.Models)
}

// SaveSnapshot never truncates an existing cache until the complete new one
// has been written and synced. A persistence failure must prevent hot reload.
func SaveSnapshot(path string, snapshot Snapshot) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(raw) > catalogMaxBytes {
		return fmt.Errorf("pricing cache too large")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pricing-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// LoadCatalog builds a fresh immutable base plus overrides and a fresh lookup
// memo, so models previously cached as unpriced become available after refresh.
func LoadCatalog(models map[string]Price, overrides map[string]Override) (*Table, error) {
	if models == nil {
		return Load(overrides)
	}
	if err := validateCatalog(models); err != nil {
		return nil, err
	}
	t := &Table{prices: make(map[string]Price, len(models)+len(overrides)), cache: map[string]*Price{}}
	for k, v := range models {
		t.prices[k] = v
	}
	applyOverrides(t, overrides)
	return t, nil
}
