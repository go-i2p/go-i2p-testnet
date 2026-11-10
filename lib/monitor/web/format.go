package web

import (
	"fmt"
	"strconv"
	"time"
)

func formatBytesPerSec(v *float64) string {
	if v == nil {
		return "—"
	}
	const unit = 1024
	val := *v
	if val < unit {
		return fmt.Sprintf("%.0f B/s", val)
	}
	div, exp := float64(unit), 0
	for n := val / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB/s", val/div, "KMGT"[exp])
}

func formatDuration(ms *int64) string {
	if ms == nil {
		return "—"
	}
	d := time.Duration(*ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	}
	return fmt.Sprintf("%dm %02ds", m, s)
}

func formatAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	default:
		return t.Local().Format("2006-01-02 15:04:05 MST")
	}
}

func statusClass(state string) string {
	switch state {
	case "up":
		return "ok"
	case "down":
		return "down"
	case "stale":
		return "stale"
	default:
		return "pending"
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func numI64(v *int64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatInt(*v, 10)
}

func numF64(v *float64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func numPtr(v *int) string {
	if v == nil {
		return "—"
	}
	return strconv.Itoa(*v)
}

// ptr returns a pointer to v (used for computed durations).
func ptr(v int64) *int64 { return &v }
