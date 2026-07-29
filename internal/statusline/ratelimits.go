package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RateLimitsFileName is where the status line drops what Claude Code told it
// about quota, for the collector to pick up (ADR-0011). A file rather than an
// HTTP call because this process is spawned on every status-line refresh and
// has a ~10ms budget (F18) — a network round trip does not fit, and quota must
// still be captured while the server is down.
const RateLimitsFileName = "rate-limits.json"

// RateLimitsFile is the on-disk hand-off. Deliberately close to the payload
// rather than to model.QuotaSnapshot: this side stays dumb, and every decision
// about scope naming and units lives in the collector that reads it.
type RateLimitsFile struct {
	Source     string                 `json:"source"`      // always "claude-code"
	ObservedAt int64                  `json:"observed_at"` // unix ms, this machine's clock
	Windows    map[string]RateLimitOn `json:"windows"`
}

// RateLimitOn is one window. ResetsAt keeps the payload's epoch SECONDS; the
// collector converts to the milliseconds the rest of the system uses.
type RateLimitOn struct {
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    int64   `json:"resets_at"`
}

// RateLimitsPath is where the file lives, beside the status line's own cache.
func RateLimitsPath(cachePath string) string {
	return filepath.Join(filepath.Dir(cachePath), RateLimitsFileName)
}

// captureRateLimits persists the quota buckets from the status-line payload.
//
// Returns nothing: the caller must not fail a render over this, and a status
// line is the wrong place to report an error to. Staleness is visible to the
// reader through ObservedAt instead.
func captureRateLimits(cfg Config, sess sessionInput, now time.Time) {
	rl := sess.RateLimits
	if rl == nil {
		return
	}
	out := RateLimitsFile{
		Source:     "claude-code",
		ObservedAt: now.UnixMilli(),
		Windows:    map[string]RateLimitOn{},
	}
	// Scope names match what the panel already renders for these windows, so
	// switching channels does not renumber anything downstream.
	for scope, w := range map[string]*rateWindow{
		"five_hour":        rl.FiveHour,
		"seven_day":        rl.SevenDay,
		"seven_day_sonnet": rl.SevenDaySonnet,
		"seven_day_opus":   rl.SevenDayOpus,
	} {
		if w == nil {
			continue
		}
		// A 0% window with no reset is a placeholder, not an observation —
		// the same guard the OAuth path needed (ccstatusline issue #343).
		if w.UsedPercentage == 0 && w.ResetsAt == 0 {
			continue
		}
		out.Windows[scope] = RateLimitOn{UsedPercent: w.UsedPercentage, ResetsAt: w.ResetsAt}
	}
	if len(out.Windows) == 0 {
		return
	}

	path := RateLimitsPath(cfg.CachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	body, err := json.Marshal(out)
	if err != nil {
		return
	}
	// Temp + rename: the collector polls this file and must never observe a
	// half-written one.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rate-limits-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
	}
}
