package statusline

import (
	"fmt"
	"strings"
)

// ANSI colours. Severity is always accompanied by the number itself, so
// colour reinforces the reading rather than carrying it.
const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
)

func render(cfg Config, sess sessionInput, d *serverData, stale bool) string {
	var parts []string
	for _, seg := range cfg.Segments {
		switch seg {
		case "session":
			if s := renderSession(sess); s != "" {
				parts = append(parts, s)
			}
		case "today":
			if d != nil && d.TodayTokens > 0 {
				s := "今日 " + compact(d.TodayTokens)
				if d.TodayCost > 0 {
					s += " " + usd(d.TodayCost)
				}
				if d.Devices > 1 {
					s += fmt.Sprintf("(%d 台)", d.Devices)
				}
				parts = append(parts, s)
			}
		case "quota":
			if d != nil {
				for _, q := range d.Quotas {
					parts = append(parts, colorize(cfg, q))
				}
			}
		}
	}
	line := strings.Join(parts, cfg.Separator)
	if stale && line != "" {
		line += " " + staleMark
	}
	return line
}

func renderSession(s sessionInput) string {
	tokens := s.ContextWindow.TotalInputTokens + s.ContextWindow.TotalOutputTokens
	if tokens == 0 && s.Cost.TotalCostUSD == 0 && s.Model.DisplayName == "" {
		return ""
	}
	var b strings.Builder
	if s.Model.DisplayName != "" {
		b.WriteString(s.Model.DisplayName)
	}
	if tokens > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(compact(tokens))
	}
	if s.Cost.TotalCostUSD > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(usd(s.Cost.TotalCostUSD))
	}
	return b.String()
}

// colorize renders one quota as "5h 97% 1h8m" with severity colour.
func colorize(cfg Config, q quotaLine) string {
	text := fmt.Sprintf("%s %.0f%%", q.Label, q.UsedPercent)
	if q.RemainMin > 0 {
		text += fmt.Sprintf(" %dh%02dm", q.RemainMin/60, q.RemainMin%60)
	}
	if cfg.NoColor {
		return text
	}
	switch {
	case q.UsedPercent >= 90:
		return ansiRed + text + ansiReset
	case q.UsedPercent >= 75:
		return ansiYellow + text + ansiReset
	default:
		return ansiDim + text + ansiReset
	}
}

func compact(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func usd(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
