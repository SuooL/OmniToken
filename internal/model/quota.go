package model

// QuotaSnapshot is an authoritative, point-in-time observation of a
// subscription quota window as reported by the provider (ADR-0007).
//
// Unlike Event (a flow that accumulates), a snapshot is state: queries take
// the latest row per (Device, Source, LimitID, WindowMinutes) and never sum.
type QuotaSnapshot struct {
	Device        string  `json:"device"`
	Source        string  `json:"source"`         // claude-code | codex
	LimitID       string  `json:"limit_id"`       // provider's limit identifier
	Scope         string  `json:"scope"`          // primary | secondary | limit-hit
	WindowMinutes int     `json:"window_minutes"` // 300 = 5h, 10080 = weekly, 0 = unknown
	UsedPercent   float64 `json:"used_percent"`
	ResetsAt      int64   `json:"resets_at"`   // unix ms; 0 = unknown
	ObservedAt    int64   `json:"observed_at"` // unix ms
	PlanType      string  `json:"plan_type,omitempty"`
}
