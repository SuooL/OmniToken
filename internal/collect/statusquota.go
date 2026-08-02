package collect

import (
	"encoding/json"
	"os"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/statusline"
)

// quotaLimitID labels the account-level Claude limit these snapshots describe.
// One account has one set of windows, so this is a constant rather than
// something read off the payload.
const statusQuotaLimitID = "claude-account"

// maxQuotaAge is how long a captured file stays worth reporting. The status
// line only runs while Claude Code does, so quota goes stale whenever the user
// stops working — past this point the numbers describe a window that has very
// likely rolled over, and re-reporting them would pin the panel to a reading
// that is no longer true.
const maxQuotaAge = 12 * time.Hour

// StatusQuotaReader turns what the status line captured into quota snapshots
// (ADR-0011).
//
// This replaced an OAuth poller that read ~/.claude/.credentials.json (falling
// back to the macOS keychain) and called Anthropic's usage endpoint. Claude
// Code already hands its status line the same account-level numbers, so the
// credentials, the HTTP client and its 429 backoff all went away with it.
//
// The trade is honest and worth stating: this channel is opportunistic. It
// only observes while Claude Code is running and refreshing its status line,
// where polling worked whether or not anyone was at the keyboard.
type StatusQuotaReader struct {
	device string
	path   string
	// lastObserved suppresses re-reporting an unchanged file every tick. The
	// store would dedupe on the primary key anyway, but a snapshot's
	// ObservedAt is meant to say "we saw this then", not "we re-read a file".
	lastObserved int64
}

func NewStatusQuotaReader(device, cachePath string) *StatusQuotaReader {
	return &StatusQuotaReader{device: device, path: statusline.RateLimitsPath(cachePath)}
}

// Path is where this reader looks, for diagnostics.
func (r *StatusQuotaReader) Path() string { return r.path }

// Collect returns snapshots for a capture not seen before. A missing file is
// not an error: it just means the status line has not run yet, which is the
// normal state until the user wires up `omnitoken statusline`.
func (r *StatusQuotaReader) Collect(now time.Time) []model.QuotaSnapshot {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return nil
	}
	var f statusline.RateLimitsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	if f.ObservedAt <= 0 || f.ObservedAt == r.lastObserved {
		return nil
	}
	if now.Sub(time.UnixMilli(f.ObservedAt)) > maxQuotaAge {
		return nil
	}

	out := make([]model.QuotaSnapshot, 0, len(f.Windows))
	for scope, w := range f.Windows {
		out = append(out, model.QuotaSnapshot{
			Device:        r.device,
			Source:        claudecode.Source,
			LimitID:       statusQuotaLimitID,
			Scope:         scope,
			WindowMinutes: windowMinutesFor(scope),
			UsedPercent:   w.UsedPercent,
			// The payload gives epoch seconds; everything else here is ms.
			ResetsAt:   secondsToMillis(w.ResetsAt),
			ObservedAt: f.ObservedAt,
		})
	}
	if len(out) == 0 {
		return nil
	}
	r.lastObserved = f.ObservedAt
	return out
}

// windowMinutesFor maps a scope to its window length. Claude's status-line
// payload names the window instead of measuring it, so the lengths are fixed
// here rather than derived.
func windowMinutesFor(scope string) int {
	switch scope {
	case "five_hour":
		return 300
	case "seven_day", "seven_day_sonnet", "seven_day_opus":
		return 10080
	default:
		return 0 // unknown; the panel labels these generically
	}
}

func secondsToMillis(sec int64) int64 {
	if sec <= 0 {
		return 0
	}
	return sec * 1000
}
