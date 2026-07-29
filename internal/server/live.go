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
)

func (s *Server) livePayload(now time.Time) (map[string]any, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	devices, err := s.store.DeviceStatuses(dayStart)
	if err != nil {
		return nil, err
	}
	type devView struct {
		store.DeviceStatus
		State string `json:"state"` // active | idle | stale
	}
	devViews := make([]devView, 0, len(devices))
	for _, d := range devices {
		state := "stale"
		age := now.UnixMilli() - d.LastTS
		if age <= deviceActive.Milliseconds() {
			state = "active"
		} else if age <= deviceStale.Milliseconds() {
			state = "idle"
		}
		devViews = append(devViews, devView{d, state})
	}
	sessions, err := s.store.ActiveSessions(now.Add(-burnWindow))
	if err != nil {
		return nil, err
	}
	total, output, err := s.store.TokensSince(now.Add(-burnWindow))
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
		"quotas":   quotaViews,
		"devices":  devViews,
		"sessions": sessions,
		"burn": map[string]any{
			"window_minutes": int(burnWindow.Minutes()),
			"tokens":         total,
			"output_tokens":  output,
			"per_minute":     total / int64(burnWindow.Minutes()),
		},
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
	payload, err := s.livePayload(time.Now())
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
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")

	send := func(event string) error {
		payload, err := s.livePayload(time.Now())
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
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": hb\n\n"); err != nil {
				return
			}
			fl.Flush()
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
