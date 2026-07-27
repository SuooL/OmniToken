package server

import (
	"net/http"
	"time"
)

// handleQuota exposes authoritative subscription quota windows (ADR-0007)
// alongside the inferred 5h blocks, so the UI can prefer real numbers and
// clearly label inferred ones.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	snaps, err := s.store.LatestQuotas(now.AddDate(0, 0, -14))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type view struct {
		Device        string  `json:"device"`
		Source        string  `json:"source"`
		Scope         string  `json:"scope"`
		WindowMinutes int     `json:"window_minutes"`
		WindowLabel   string  `json:"window_label"`
		UsedPercent   float64 `json:"used_percent"`
		ResetsAt      int64   `json:"resets_at"`
		ObservedAt    int64   `json:"observed_at"`
		PlanType      string  `json:"plan_type,omitempty"`
		Expired       bool    `json:"expired"` // window already reset since we observed it
	}
	out := make([]view, 0, len(snaps))
	for _, q := range snaps {
		out = append(out, view{
			Device: q.Device, Source: q.Source, Scope: q.Scope,
			WindowMinutes: q.WindowMinutes, WindowLabel: windowLabel(q.WindowMinutes),
			UsedPercent: q.UsedPercent, ResetsAt: q.ResetsAt, ObservedAt: q.ObservedAt,
			PlanType: q.PlanType,
			Expired:  q.ResetsAt > 0 && q.ResetsAt < now.UnixMilli(),
		})
	}
	writeJSON(w, map[string]any{"quotas": out, "generated_at": now.UnixMilli()})
}

func windowLabel(minutes int) string {
	switch {
	case minutes == 0:
		return "未知窗口"
	case minutes%10080 == 0:
		return "周窗口"
	case minutes%1440 == 0:
		return "日窗口"
	case minutes%60 == 0:
		return itoa(minutes/60) + " 小时窗口"
	default:
		return itoa(minutes) + " 分钟窗口"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
