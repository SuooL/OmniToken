package collect

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/antigravity"
	"github.com/suool/omnitoken/internal/parser/claudecode"
	"github.com/suool/omnitoken/internal/statusline"
)

const (
	claudeQuotaLimitID      = "claude-account"
	antigravityQuotaLimitID = "antigravity-account"
)

// maxQuotaAge is how long a captured file stays worth reporting. The status
// line only runs while the CLI does, so quota goes stale whenever the user
// stops working — past this point the numbers describe a window that has very
// likely rolled over, and re-reporting them would pin the panel to a reading
// that is no longer true.
const maxQuotaAge = 12 * time.Hour

type quotaSourceFile struct {
	path          string
	defaultSource string
	limitID       string
	lastObserved  int64
}

// StatusQuotaReader turns what the status line captured into quota snapshots
// (ADR-0011 / ADR-0045). Reads both Claude Code's `rate-limits.json` and
// Antigravity CLI's `antigravity-rate-limits.json` without credentials or
// polling endpoints.
type StatusQuotaReader struct {
	device string
	files  []quotaSourceFile
}

func NewStatusQuotaReader(device, cachePath string) *StatusQuotaReader {
	return &StatusQuotaReader{
		device: device,
		files: []quotaSourceFile{
			{
				path:          statusline.RateLimitsPath(cachePath),
				defaultSource: claudecode.Source,
				limitID:       claudeQuotaLimitID,
			},
			{
				path:          statusline.AntigravityRateLimitsPath(cachePath),
				defaultSource: antigravity.Source,
				limitID:       antigravityQuotaLimitID,
			},
		},
	}
}

// Path is where the primary Claude Code capture file lives, for diagnostics.
func (r *StatusQuotaReader) Path() string {
	if len(r.files) == 0 {
		return ""
	}
	return r.files[0].path
}

// Collect returns snapshots for captures not seen before across Claude Code
// and Antigravity CLI, immediately advancing each file's high-water mark.
// Prefer CollectAndDeliver when delivery can fail so unacknowledged captures
// are retried on the next tick.
func (r *StatusQuotaReader) Collect(now time.Time) []model.QuotaSnapshot {
	out, pending := r.peek(now)
	r.commit(pending)
	return out
}

// CollectAndDeliver reads unseen captures, invokes sink, and only advances each
// file's lastObserved watermark after sink returns nil. Missing files are
// normal until the hook is wired up.
func (r *StatusQuotaReader) CollectAndDeliver(now time.Time, sink func([]model.QuotaSnapshot) error) error {
	out, pending := r.peek(now)
	if len(out) == 0 {
		return nil
	}
	if err := sink(out); err != nil {
		return err
	}
	r.commit(pending)
	return nil
}

func (r *StatusQuotaReader) peek(now time.Time) ([]model.QuotaSnapshot, []int64) {
	var out []model.QuotaSnapshot
	pending := make([]int64, len(r.files))
	for i := range r.files {
		src := &r.files[i]
		raw, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		var f statusline.RateLimitsFile
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		if f.ObservedAt <= 0 || f.ObservedAt == src.lastObserved {
			continue
		}
		if now.Sub(time.UnixMilli(f.ObservedAt)) > maxQuotaAge {
			continue
		}
		source := strings.TrimSpace(f.Source)
		if source == "" {
			source = src.defaultSource
		}
		limitID := src.limitID
		if source == antigravity.Source {
			limitID = antigravityQuotaLimitID
		}
		added := 0
		for scope, w := range f.Windows {
			out = append(out, model.QuotaSnapshot{
				Device:        r.device,
				Source:        source,
				LimitID:       limitID,
				Scope:         scope,
				WindowMinutes: windowMinutesFor(scope),
				UsedPercent:   w.UsedPercent,
				// The payload gives epoch seconds; everything else here is ms.
				ResetsAt:   secondsToMillis(w.ResetsAt),
				ObservedAt: f.ObservedAt,
			})
			added++
		}
		if added > 0 {
			pending[i] = f.ObservedAt
		}
	}
	return out, pending
}

func (r *StatusQuotaReader) commit(pending []int64) {
	for i, ts := range pending {
		if ts > 0 && i < len(r.files) {
			r.files[i].lastObserved = ts
		}
	}
}

// windowMinutesFor maps a scope to its window length. Both Claude Code and
// Antigravity CLI name their buckets by duration rather than sending a length.
func windowMinutesFor(scope string) int {
	s := strings.ToLower(strings.TrimSpace(scope))
	switch s {
	case "five_hour", "gemini-5h", "3p-5h":
		return 300
	case "seven_day", "seven_day_sonnet", "seven_day_opus",
		"gemini-weekly", "gemini-argon-wtu-weekly", "3p-weekly":
		return 10080
	}
	if strings.HasSuffix(s, "-5h") || strings.Contains(s, "five_hour") {
		return 300
	}
	if strings.HasSuffix(s, "-weekly") || strings.Contains(s, "seven_day") {
		return 10080
	}
	return 0 // unknown; the panel labels these generically
}

func secondsToMillis(sec int64) int64 {
	if sec <= 0 {
		return 0
	}
	return sec * 1000
}
