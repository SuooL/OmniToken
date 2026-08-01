package server

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"time"
)

const (
	heartbeatOnlineWindow = 2 * time.Minute
	heartbeatStaleWindow  = 10 * time.Minute
)

// deviceEntry is one row of the devices page (F21 / GAP-3): range totals plus
// the context that makes a device comparable — today's volume, when it last
// reported, how many projects it touched, and its dominant model.
type deviceEntry struct {
	Device          string   `json:"device"`
	DeviceID        string   `json:"device_id,omitempty"`
	DisplayName     string   `json:"display_name,omitempty"`
	IdentityStatus  string   `json:"identity_status"`
	ConnectionState string   `json:"connection_state"`
	LastSeenAt      int64    `json:"last_seen_at,omitempty"`
	LastSeenAgeMS   *int64   `json:"last_seen_age_ms,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	QueuedBatches   int      `json:"queued_batches,omitempty"`
	QueuedBytes     int64    `json:"queued_bytes,omitempty"`
	OldestQueuedAt  int64    `json:"oldest_queued_at,omitempty"`
	Events          int64    `json:"events"`
	TotalTokens     int64    `json:"total_tokens"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CacheRead       int64    `json:"cache_read_tokens"`
	CacheCreation   int64    `json:"cache_creation_tokens"`
	TodayTokens     int64    `json:"today_tokens"`
	LastTS          int64    `json:"last_ts"`
	Repos           int64    `json:"repos"`
	TopModel        string   `json:"top_model"`
	TopModelTokens  int64    `json:"top_model_tokens"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	CostPartial     bool     `json:"cost_partial,omitempty"`
}

// handleDevices answers GET /api/v1/devices?days=30 with per-device summary
// rows and the (device, day) matrix behind the stacked trend.
//
// Cost is computed per device from its per-model breakdown (ADR-0005). A
// device whose models are all unpriced omits cost_usd entirely rather than
// reporting $0 — "unpriced" and "free" are different facts; when only some
// models are unpriced the figure is a lower bound and cost_partial says so.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	now := s.currentTime()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rangeStart := dayStart.AddDate(0, 0, -(days - 1))
	end := now.Add(time.Hour)

	summary, err := s.store.DeviceSummary(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	daily, err := s.store.DeviceDaily(rangeStart, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Today's volume comes from the live-view query so both pages agree.
	statuses, err := s.store.DeviceStatuses(dayStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	today := make(map[string]int64, len(statuses))
	for _, st := range statuses {
		today[st.Device] = st.TodayTokens
	}

	unpricedSet := map[string]bool{}
	out := make([]deviceEntry, 0, len(summary))
	indexByDevice := make(map[string]int, len(summary))
	for _, row := range summary {
		e := deviceEntry{
			Device: row.Device, Events: row.Events, TotalTokens: row.TotalTokens,
			InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
			CacheRead: row.CacheRead, CacheCreation: row.CacheCreation,
			TodayTokens: today[row.Device], LastTS: row.LastTS, Repos: row.Repos,
			TopModel: row.TopModel, TopModelTokens: row.TopModelTokens,
			IdentityStatus: "legacy_unbound", ConnectionState: "unknown",
		}
		var cost float64
		priced, missing := 0, 0
		for _, m := range row.Models {
			c, ok := s.Prices().Cost(m.Model, time.UnixMilli(m.MinTS),
				m.InputTokens, m.OutputTokens, m.CacheRead, m.CacheCreation, m.Cache1h, m.Cache5m)
			if !ok {
				missing++
				unpricedSet[m.Model] = true
				continue
			}
			priced++
			cost += c
		}
		if priced > 0 {
			e.CostUSD = &cost
			e.CostPartial = missing > 0
		}
		indexByDevice[e.Device] = len(out)
		out = append(out, e)
	}

	registered, err := s.store.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, record := range registered {
		index, exists := indexByDevice[record.DeviceID]
		if !exists {
			index = len(out)
			indexByDevice[record.DeviceID] = index
			out = append(out, deviceEntry{Device: record.DeviceID})
		}
		entry := &out[index]
		entry.DeviceID = record.DeviceID
		entry.IdentityStatus = "registered"
		entry.Capabilities = append([]string(nil), record.Capabilities...)
		entry.LastSeenAt = record.LastSeenAt
		entry.ConnectionState = heartbeatState(now, record.LastSeenAt, record.RevokedAt)
		if record.LastSeenAt > 0 {
			age := max(now.UnixMilli()-record.LastSeenAt, 0)
			entry.LastSeenAgeMS = &age
		}
		heartbeat, _, heartbeatErr := s.store.LatestHeartbeat(record.DeviceID)
		if heartbeatErr == nil {
			entry.QueuedBatches = heartbeat.QueuedBatches
			entry.QueuedBytes = heartbeat.QueuedBytes
			entry.OldestQueuedAt = heartbeat.OldestQueuedAt
		} else if !errors.Is(heartbeatErr, sql.ErrNoRows) {
			http.Error(w, heartbeatErr.Error(), http.StatusInternalServerError)
			return
		}
	}

	// One resolution for every device-keyed view, applied last so a rename
	// reaches registered and legacy identities alike (see deviceNames).
	names, err := s.deviceNames()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range out {
		out[i].DisplayName = names.name(out[i].Device)
	}

	unpriced := make([]string, 0, len(unpricedSet))
	for m := range unpricedSet {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)

	writeJSON(w, map[string]any{
		"days":         days,
		"summary":      out,
		"daily":        daily,
		"unpriced":     unpriced,
		"generated_at": now.UnixMilli(),
	})
}

func heartbeatState(now time.Time, lastSeenAt, revokedAt int64) string {
	if revokedAt > 0 || lastSeenAt <= 0 {
		return "offline"
	}
	age := now.UnixMilli() - lastSeenAt
	switch {
	case age <= heartbeatOnlineWindow.Milliseconds():
		return "online"
	case age <= heartbeatStaleWindow.Milliseconds():
		return "stale"
	default:
		return "offline"
	}
}
