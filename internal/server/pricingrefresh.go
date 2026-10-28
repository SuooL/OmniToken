package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/suool/omnitoken/internal/pricing"
)

type pricingRefreshStatus struct {
	Enabled        bool   `json:"enabled"`
	Source         string `json:"source"`
	IntervalHours  int    `json:"interval_hours"`
	ActiveSnapshot string `json:"active_snapshot"`
	ModelCount     int    `json:"model_count"`
	LastAttemptMS  int64  `json:"last_attempt_at_ms,omitempty"`
	LastSuccessMS  int64  `json:"last_success_at_ms,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

func (c *Config) pricingUpdatesEnabled() bool {
	return c.PricingAutoUpdate == nil || *c.PricingAutoUpdate
}
func (c *Config) pricingRefreshInterval() time.Duration {
	if c.PricingRefreshHours == 0 {
		return 24 * time.Hour
	}
	return time.Duration(c.PricingRefreshHours) * time.Hour
}
func (s *Server) pricingCachePath() string {
	return filepath.Join(filepath.Dir(s.cfg.DBPath), "pricing-cache.json")
}

// Called before serving; invalid caches fall back to embedded prices. The
// source error remains visible until a complete refresh succeeds.
func (s *Server) loadPricingSnapshot() {
	snapshot, err := pricing.ReadSnapshot(s.pricingCachePath())
	if err != nil {
		if !os.IsNotExist(err) {
			s.pricingStatus.LastError = "cache: " + err.Error()
			log.Printf("pricing: cache rejected: %v; using embedded prices", err)
		}
		return
	}
	s.pricingCatalog = snapshot.Models
	s.pricingStatus.LastSuccessMS = snapshot.FetchedAt.UnixMilli()
	s.pricingStatus.ActiveSnapshot = "cache"
}

func (s *Server) pricingUpdateStatus() pricingRefreshStatus {
	s.pricingUpdateMu.Lock()
	defer s.pricingUpdateMu.Unlock()
	status := s.pricingStatus
	status.Enabled = s.cfg.pricingUpdatesEnabled()
	status.Source = pricing.CatalogURL
	status.IntervalHours = int(s.cfg.pricingRefreshInterval() / time.Hour)
	if status.ActiveSnapshot == "" {
		status.ActiveSnapshot = "embedded"
	}
	if s.pricingCatalog != nil {
		status.ModelCount = len(s.pricingCatalog)
	} else {
		status.ModelCount = s.Prices().ModelCount()
	}
	return status
}

func (s *Server) pricingRefreshDelay(now time.Time) time.Duration {
	s.pricingUpdateMu.Lock()
	defer s.pricingUpdateMu.Unlock()
	status := s.pricingStatus
	next := time.UnixMilli(status.LastSuccessMS).Add(s.cfg.pricingRefreshInterval())
	if status.LastError != "" && status.LastAttemptMS > 0 && status.LastAttemptMS >= status.LastSuccessMS {
		next = time.UnixMilli(status.LastAttemptMS).Add(min(time.Hour, s.cfg.pricingRefreshInterval()))
	} else if status.LastSuccessMS == 0 {
		return 0
	}
	return max(time.Duration(0), next.Sub(now))
}

func (s *Server) runPricingUpdates(ctx context.Context) {
	if !s.cfg.pricingUpdatesEnabled() {
		return
	}
	for {
		timer := time.NewTimer(s.pricingRefreshDelay(s.currentTime()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.refreshPricing(ctx, http.DefaultClient); err != nil {
			log.Printf("pricing: refresh failed (keeping previous prices): %v", err)
		}
	}
}

// Fetch outside the settings-reload lock so a slow network never blocks
// settings saves. Serialize refreshes and apply the latest overrides at commit.
func (s *Server) refreshPricing(ctx context.Context, client *http.Client) error {
	s.pricingFetchMu.Lock()
	defer s.pricingFetchMu.Unlock()
	s.pricingUpdateMu.Lock()
	s.pricingStatus.LastAttemptMS = s.currentTime().UnixMilli()
	s.pricingUpdateMu.Unlock()
	models, err := pricing.FetchCatalog(ctx, client)
	s.pricingUpdateMu.Lock()
	defer s.pricingUpdateMu.Unlock()
	if err != nil {
		s.pricingStatus.LastError = err.Error()
		return err
	}
	table, err := s.priceTable(models)
	if err == nil {
		err = pricing.SaveSnapshot(s.pricingCachePath(), pricing.Snapshot{FetchedAt: s.currentTime(), Models: models})
	}
	if err != nil {
		s.pricingStatus.LastError = err.Error()
		return err
	}
	s.pricingCatalog = models
	s.setPrices(table)
	s.invalidatePriceCaches()
	s.pricingStatus.LastSuccessMS = s.currentTime().UnixMilli()
	s.pricingStatus.LastError = ""
	s.pricingStatus.ActiveSnapshot = "upstream"
	s.bcast.Notify()
	log.Printf("pricing: refreshed %d models from LiteLLM; next check in %s", len(models), s.cfg.pricingRefreshInterval())
	return nil
}

func (s *Server) invalidatePriceCaches() {
	if s.readCache != nil {
		s.readCache.clear()
	}
}
