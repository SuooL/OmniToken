package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/suool/omnitoken/internal/store"
)

// broadcaster fans "data changed" signals out to SSE subscribers. Contract
// follows token-monitor's hub (references.md): both write paths (HTTP ingest
// AND the in-process collectors) must call Notify — the broadcast hooks the
// storage layer, not the transport.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[chan struct{}]struct{}{}}
}

func (b *broadcaster) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default: // subscriber already has a pending signal
		}
	}
}

func (b *broadcaster) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *broadcaster) hasSubscribers() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs) > 0
}

const (
	burnWindow   = 10 * time.Minute
	deviceActive = 2 * time.Minute
	deviceStale  = 10 * time.Minute // token-monitor's staleAfterMs default
	// How long a process report stays trustworthy (ADR-0012). Six times the
	// default 15s collection interval: long enough that a slow tick or a brief
	// network drop does not blank the panel, short enough that a machine which
	// went away stops claiming to have sessions open.
	procTTL = 90 * time.Second
)

func (s *Server) livePayload(now time.Time) (map[string]any, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	devices, err := s.store.DeviceStatuses(dayStart)
	if err != nil {
		return nil, err
	}
	// Process ground truth (ADR-0012), fetched before the device views so each
	// device can say whether it has any.
	running, err := s.store.RunningSessions(now.Add(-procTTL))
	if err != nil {
		return nil, err
	}
	reporters, err := s.store.ProcReporters(now.Add(-procTTL))
	if err != nil {
		return nil, err
	}
	reports := make(map[string]bool, len(reporters))
	for _, r := range reporters {
		reports[r.Device] = true
	}
	procCount := map[string]int{}
	for _, p := range running {
		procCount[p.Device]++
	}

	type devView struct {
		store.DeviceStatus
		State           string `json:"state"` // active | idle | stale (legacy UI compatibility)
		DeviceID        string `json:"device_id,omitempty"`
		DisplayName     string `json:"display_name,omitempty"`
		IdentityStatus  string `json:"identity_status"`
		ConnectionState string `json:"connection_state"`
		LastSeenAt      int64  `json:"last_seen_at,omitempty"`
		LastSeenAgeMS   *int64 `json:"last_seen_age_ms,omitempty"`
		// Running is meaningless unless HasProcs is true: a device with no
		// agent (SSH-pulled) reports nothing, which is not the same as zero
		// sessions and must not be rendered as "closed".
		HasProcs bool `json:"has_procs"`
		Running  int  `json:"running"`
	}
	devViews := make([]devView, 0, len(devices))
	viewByDevice := make(map[string]int, len(devices))
	for _, d := range devices {
		state := "stale"
		age := now.UnixMilli() - d.LastTS
		if age <= deviceActive.Milliseconds() {
			state = "active"
		} else if age <= deviceStale.Milliseconds() {
			state = "idle"
		}
		viewByDevice[d.Device] = len(devViews)
		devViews = append(devViews, devView{
			DeviceStatus:    d,
			State:           state,
			IdentityStatus:  s.identityStatusFor(d.Device),
			ConnectionState: "unknown",
			HasProcs:        reports[d.Device],
			Running:         procCount[d.Device],
		})
	}
	registered, err := s.store.ListDevices()
	if err != nil {
		return nil, err
	}
	for _, record := range registered {
		index, exists := viewByDevice[record.DeviceID]
		if !exists {
			index = len(devViews)
			viewByDevice[record.DeviceID] = index
			devViews = append(devViews, devView{
				DeviceStatus: store.DeviceStatus{Device: record.DeviceID},
				HasProcs:     reports[record.DeviceID],
				Running:      procCount[record.DeviceID],
			})
		}
		view := &devViews[index]
		view.DeviceID = record.DeviceID
		view.IdentityStatus = "registered"
		view.ConnectionState = heartbeatState(now, record.LastSeenAt, record.RevokedAt)
		view.LastSeenAt = record.LastSeenAt
		if record.LastSeenAt > 0 {
			age := max(now.UnixMilli()-record.LastSeenAt, 0)
			view.LastSeenAgeMS = &age
		}
		switch view.ConnectionState {
		case "online":
			view.State = "active"
		case "stale":
			view.State = "idle"
		default:
			view.State = "stale"
		}
	}
	// Same resolution the devices page uses, so one machine cannot end up with
	// two names depending on which page is open (see deviceNames).
	names, err := s.deviceNames()
	if err != nil {
		return nil, err
	}
	for i := range devViews {
		devViews[i].DisplayName = names.name(devViews[i].Device)
	}
	sessions, err := s.store.ActiveSessions(now.Add(-burnWindow))
	if err != nil {
		return nil, err
	}
	total, output, err := s.store.TokensSince(now.Add(-burnWindow))
	if err != nil {
		return nil, err
	}
	// Same window as burn so "active" means one thing across the payload.
	// Widening it would not dilute the rate anyway: speed divides by the union
	// of generation intervals, not by the window (ADR-0009).
	speed, err := s.store.LiveSpeedSince(now.Add(-burnWindow), now, "")
	if err != nil {
		return nil, err
	}

	quotas, err := s.store.LatestQuotas(now.Add(-14 * 24 * time.Hour))
	if err != nil {
		return nil, err
	}
	// 5-hour view, per billing channel (see buildWindowCards).
	windows, err := s.buildWindowCards(now, quotas)
	if err != nil {
		return nil, err
	}
	quotaViews := make([]map[string]any, 0, len(quotas))
	for _, q := range quotas {
		if q.ResetsAt > 0 && q.ResetsAt < now.UnixMilli() {
			continue // window already reset; the number is stale
		}
		quotaViews = append(quotaViews, map[string]any{
			"source": q.Source, "scope": q.Scope, "window_label": windowLabel(q.WindowMinutes),
			"window_minutes": q.WindowMinutes, "used_percent": q.UsedPercent,
			"resets_at": q.ResetsAt, "observed_at": q.ObservedAt, "device": q.Device,
			"remaining_minutes": int(max(q.ResetsAt-now.UnixMilli(), 0) / 60000),
		})
	}

	return map[string]any{
		"quotas":  quotaViews,
		"devices": devViews,
		// Inferred from recent events: "used tokens lately". Kept beside
		// `processes` on purpose — the two answer different questions, and a
		// session can appear in one and not the other (open but thinking; or
		// just closed after a burst).
		"sessions": sessions,
		"processes": map[string]any{
			"sessions":    running,
			"reporters":   reporters,
			"ttl_seconds": int(procTTL.Seconds()),
		},
		"burn": map[string]any{
			"window_minutes": int(burnWindow.Minutes()),
			"tokens":         total,
			"output_tokens":  output,
			"per_minute":     total / int64(burnWindow.Minutes()),
		},
		// Distinct from burn on purpose: burn divides by the whole window and
		// so includes idle time ("how much am I consuming"), speed divides by
		// the union of generation intervals ("how fast is it generating").
		"speed":        speed,
		"windows":      windows,
		"generated_at": now.UnixMilli(),
	}, nil
}

// handleLive serves the same payload as the SSE snapshot, once, over plain GET.
//
// It exists for the menubar client, which polls rather than holding a stream
// open (ADR-0008 defers the SSE bridge past v1). Sharing livePayload is the
// point: burn rate is defined once, so the popover and the Live page cannot
// drift into reporting different numbers for the same ten minutes.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	payload, err := s.livePayload(s.currentTime())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, payload)
}

// handleStream is the SSE endpoint (docs/API.md). token-monitor-aligned:
// snapshot on connect, x-accel-buffering off, 30s comment heartbeats,
// ≥1s coalescing of change signals.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// The server-wide write deadline protects every bounded response from slow
	// readers. SSE is the one intentional exception: it owns the connection
	// until the client leaves and emits a bounded state-bearing refresh below.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")

	send := func(event string) error {
		payload, err := s.livePayload(s.currentTime())
		if err != nil {
			return err
		}
		data, _ := json.Marshal(payload)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		fl.Flush()
		return err
	}
	if err := send("snapshot"); err != nil {
		return
	}
	ch, cancel := s.bcast.Subscribe()
	defer cancel()
	refreshInterval := s.streamRefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = 30 * time.Second
	}
	refresh := time.NewTicker(refreshInterval)
	defer refresh.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-refresh.C:
			// A comment proves transport liveness but leaves fleet state frozen.
			// Recompute the payload so last-seen thresholds age even when no
			// collector mutation occurs.
			if err := send("live"); err != nil {
				return
			}
		case <-ch:
			time.Sleep(time.Second) // coalesce bursts; pending signals collapse
			select {
			case <-ch:
			default:
			}
			if err := send("live"); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	now := time.Now()
	blocks, err := s.store.Blocks(now.AddDate(0, 0, -days), now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"blocks": blocks, "duration_hours": 5})
}
