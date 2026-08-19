package model

import (
	"fmt"
	"strings"
	"time"
)

// FormatCPU renders milli-cores the way kubectl top does below one core, and
// switches to fractional cores above it where "2.4" reads faster than "2400m".
func FormatCPU(milli int64) string {
	if milli < 1000 {
		return fmt.Sprintf("%dm", milli)
	}
	cores := float64(milli) / 1000
	if cores < 10 {
		return fmt.Sprintf("%.2f", cores)
	}
	return fmt.Sprintf("%.1f", cores)
}

// FormatBytes renders a byte count in binary units, matching the Mi/Gi
// vocabulary of Kubernetes resource specs rather than SI megabytes.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	suffix := []string{"Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}[exp]
	if v >= 100 {
		return fmt.Sprintf("%.0f%s", v, suffix)
	}
	return fmt.Sprintf("%.1f%s", v, suffix)
}

// FormatMetric renders a raw value in the unit appropriate to its metric.
func FormatMetric(m Metric, v int64) string {
	if m == MetricCPU {
		return FormatCPU(v)
	}
	return FormatBytes(v)
}

// FormatPercent renders a fraction as a percentage, or a dash when the
// denominator does not exist.
func FormatPercent(f float64, ok bool) string {
	if !ok {
		return "-"
	}
	p := f * 100
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.0f%%", p)
	case p >= 100:
		return fmt.Sprintf("%.0f%%", p)
	default:
		return fmt.Sprintf("%.0f%%", p)
	}
}

// FormatAge renders a duration in kubectl's compact style (5d, 3h20m, 45s).
func FormatAge(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 || days > 7 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// FormatLabels renders a label map deterministically for display.
func FormatLabels(l map[string]string) string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	// sort.Strings without importing sort twice in this file
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+l[k])
	}
	return strings.Join(parts, ",")
}
