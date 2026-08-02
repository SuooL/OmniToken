package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/suool/omnitoken/internal/pricing"
	"github.com/suool/omnitoken/internal/store"
)

// Settings API (F23/GAP-5): pricing overrides and device display names,
// editable from the panel and effective **without a restart**.
//
// All amounts are USD, everywhere: pricing input, computation and display.
// There is no display-currency conversion — see docs/roadmap.md for why.
//
// Reads are open like the other query APIs; the write goes through s.auth —
// pricing overrides change every cost number on the panel, so it is a write in
// the sense that matters. Nothing here mutates stored events: costs are
// computed at query time (ADR-0005), so an override applies retroactively to
// history and is undone by simply removing it.
const (
	settingsKeyPricing = "pricing_overrides"
	// The label key is owned by the store: a device merge has to move labels
	// inside its own transaction, so both sides must name the same document.
	settingsKeyDeviceLabels = store.DeviceLabelsKey
)

// Validation bounds. Prices are per 1M tokens in USD: 10000 is far above any
// real model yet still catches the "typed per-token instead of per-1M" and
// stray-zero classes of mistake.
const (
	maxPricePerMTok  = 10000.0
	maxDeviceLabel   = 64 // runes
	maxModelNameLen  = 128
	settingsBodyMax  = 1 << 20
	settingsSavedMsg = "saved"
)

// pricesMu guards Server.prices against the hot swap done by
// handlePutSettings. It lives here (package-level) so this file compiles
// without touching server.go; moving it into the Server struct is the
// preferred end state — see the integration notes.
//
// NOTE: this protects the *pointer*, not pricing.Table's internal lookup memo,
// which is written by Lookup() and is already raced on by concurrent request
// handlers today. Fixing that belongs in internal/pricing.
var pricesMu sync.RWMutex

// Prices returns the live price table. All cost paths should read through
// this rather than touching s.prices directly, so a settings save can swap
// the table under them safely.
func (s *Server) Prices() *pricing.Table {
	pricesMu.RLock()
	defer pricesMu.RUnlock()
	return s.prices
}

func (s *Server) setPrices(t *pricing.Table) {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	s.prices = t
}

// ReloadPricing rebuilds the price table from config overrides plus the
// DB-stored ones (DB wins on conflict) and swaps it in atomically. Call it
// from New() so DB overrides apply at startup, and after every settings save.
func (s *Server) ReloadPricing() error {
	merged := map[string]pricing.Override{}
	for k, v := range s.cfg.PricingOverrides {
		merged[k] = v
	}
	var stored map[string]pricing.Override
	if err := s.store.GetSettingsJSON(settingsKeyPricing, &stored); err != nil {
		return err
	}
	for k, v := range stored {
		merged[k] = v
	}
	table, err := pricing.Load(merged)
	if err != nil {
		return err
	}
	s.setPrices(table)
	return nil
}

type settingsResponse struct {
	PricingOverrides map[string]pricing.Override `json:"pricing_overrides"`
	DeviceLabels     map[string]string           `json:"device_labels"`
	// DeviceMerges is the audit log of ADR-0019 merges, oldest first. It is
	// read-only and has no delete counterpart: a merge cannot be undone, so the
	// record of it is the only thing left to check against afterwards.
	DeviceMerges []store.DeviceMergeRecord `json:"device_merges"`
	// LocalIdentity lets the panel warn that this machine is in the database
	// under two names before the user goes looking for the reason.
	LocalIdentity localIdentity `json:"local_identity"`
}

// settingsRequest uses pointers so an absent field means "leave as is" while
// an empty object means "replace with nothing" — the panel needs the latter to
// delete the last override or label.
type settingsRequest struct {
	PricingOverrides *map[string]pricing.Override `json:"pricing_overrides"`
	DeviceLabels     *map[string]string           `json:"device_labels"`
}

// currentSettings reads all three documents, applying defaults for unset keys.
// Only overrides stored here are returned; overrides written into config.json
// stay in force but are not editable from the panel (the panel says so).
func (s *Server) currentSettings() (settingsResponse, error) {
	resp := settingsResponse{
		PricingOverrides: map[string]pricing.Override{},
		DeviceLabels:     map[string]string{},
	}
	if err := s.store.GetSettingsJSON(settingsKeyPricing, &resp.PricingOverrides); err != nil {
		return resp, err
	}
	if err := s.store.GetSettingsJSON(settingsKeyDeviceLabels, &resp.DeviceLabels); err != nil {
		return resp, err
	}
	if resp.PricingOverrides == nil {
		resp.PricingOverrides = map[string]pricing.Override{}
	}
	if resp.DeviceLabels == nil {
		resp.DeviceLabels = map[string]string{}
	}
	merges, err := s.store.DeviceMergeHistory()
	if err != nil {
		return resp, err
	}
	// Always an array, never null: the panel renders "还没有合并过" from an empty
	// list and would otherwise have to special-case a missing field.
	resp.DeviceMerges = merges
	if resp.DeviceMerges == nil {
		resp.DeviceMerges = []store.DeviceMergeRecord{}
	}
	identity, err := s.localIdentity()
	if err != nil {
		return resp, err
	}
	resp.LocalIdentity = identity
	return resp, nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	resp, err := s.currentSettings()
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

// handlePutSettings validates then persists each present section, and hot
// reloads the price table when pricing changed. Validation is all-or-nothing:
// nothing is written unless every field passes, so a bad row can't leave the
// settings half-applied.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, settingsBodyMax)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	var overrides map[string]pricing.Override
	if req.PricingOverrides != nil {
		normalized, err := validatePricingOverrides(*req.PricingOverrides)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		overrides = normalized
	}
	var labels map[string]string
	if req.DeviceLabels != nil {
		normalized, err := validateDeviceLabels(*req.DeviceLabels)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		labels = normalized
	}

	if overrides != nil {
		if err := s.store.SetSettingsJSON(settingsKeyPricing, overrides); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Hot reload: costs are computed at query time, so swapping the table
		// makes the new prices apply to all history on the next request.
		if err := s.ReloadPricing(); err != nil {
			http.Error(w, "pricing reload: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if labels != nil {
		if err := s.store.SetSettingsJSON(settingsKeyDeviceLabels, labels); err != nil {
			http.Error(w, "store: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	resp, err := s.currentSettings()
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Tell the panel a repaint is worthwhile: every cost on screen just moved.
	if overrides != nil {
		s.bcast.Notify()
	}
	writeJSON(w, map[string]any{
		"status":           settingsSavedMsg,
		"pricing_reloaded": overrides != nil,
		"settings":         resp,
	})
}

// validatePricingOverrides lowercases model keys (pricing.Load matches on
// lowercase) and rejects out-of-range prices, naming the offending field.
func validatePricingOverrides(in map[string]pricing.Override) (map[string]pricing.Override, error) {
	out := make(map[string]pricing.Override, len(in))
	for name, o := range in {
		model := strings.ToLower(strings.TrimSpace(name))
		if model == "" {
			return nil, fmt.Errorf("定价覆盖:模型名不能为空")
		}
		if len(model) > maxModelNameLen {
			return nil, fmt.Errorf("定价覆盖:模型名 %q 超过 %d 字符", name, maxModelNameLen)
		}
		if _, dup := out[model]; dup {
			return nil, fmt.Errorf("定价覆盖:模型 %q 重复(模型名不区分大小写)", model)
		}
		for _, f := range []struct {
			label string
			v     float64
		}{
			{"input_per_mtok", o.InputPerM},
			{"output_per_mtok", o.OutputPerM},
			{"cache_read_per_mtok", o.CacheReadPerM},
			{"cache_write_per_mtok", o.CacheWritePerM},
		} {
			if math.IsNaN(f.v) || math.IsInf(f.v, 0) {
				return nil, fmt.Errorf("定价覆盖:模型 %q 的 %s 不是有效数字", model, f.label)
			}
			if f.v < 0 || f.v > maxPricePerMTok {
				return nil, fmt.Errorf("定价覆盖:模型 %q 的 %s = %g 非法,须在 0 到 %g 之间(单位:美元/百万 token)",
					model, f.label, f.v, maxPricePerMTok)
			}
		}
		out[model] = o
	}
	return out, nil
}

// validateDeviceLabels maps hostname → display name. An empty label is a
// deletion at the panel level, so it must not reach the store as a blank name.
func validateDeviceLabels(in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for host, label := range in {
		h := strings.TrimSpace(host)
		if h == "" {
			return nil, fmt.Errorf("设备重命名:设备标识不能为空")
		}
		l := strings.TrimSpace(label)
		if l == "" {
			return nil, fmt.Errorf("设备重命名:设备 %q 的显示名不能为空(要取消重命名请删除该项)", h)
		}
		if utf8.RuneCountInString(l) > maxDeviceLabel {
			return nil, fmt.Errorf("设备重命名:设备 %q 的显示名超过 %d 字符", h, maxDeviceLabel)
		}
		out[h] = l
	}
	return out, nil
}
