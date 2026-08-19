// Package render turns rows into something a terminal can display: colored
// bars, aligned tables, and machine-readable output.
package render

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Severity buckets a usage fraction into a color band.
type Severity int

const (
	SevUnknown Severity = iota
	SevIdle
	SevNormal
	SevElevated
	SevHigh
	SevCritical
	SevOver
)

// Thresholds define where each band starts, as a fraction of the denominator.
//
// The defaults are chosen around what actually goes wrong in Kubernetes rather
// than round numbers: sustained CPU above 80% of a limit means throttling is
// already happening, and memory above 90% of a limit means the OOM killer is
// one traffic spike away. Anything at or over 100% gets its own band because
// it is qualitatively different from "nearly full".
type Thresholds struct {
	Idle     float64
	Normal   float64
	Elevated float64
	High     float64
	Critical float64
}

// DefaultThresholds is the out-of-the-box color scale.
var DefaultThresholds = Thresholds{
	Idle:     0.05,
	Normal:   0.50,
	Elevated: 0.70,
	High:     0.85,
	Critical: 0.95,
}

// Classify buckets a fraction. ok=false (no denominator) yields SevUnknown, so
// a pod with no limit is drawn in muted gray rather than a misleading green.
func (t Thresholds) Classify(f float64, ok bool) Severity {
	if !ok {
		return SevUnknown
	}
	switch {
	case f >= 1.0:
		return SevOver
	case f >= t.Critical:
		return SevCritical
	case f >= t.High:
		return SevHigh
	case f >= t.Elevated:
		return SevElevated
	case f >= t.Normal:
		return SevNormal
	case f >= t.Idle:
		return SevIdle
	default:
		return SevIdle
	}
}

// Palette maps severities to styles.
//
// AdaptiveColor is used throughout so the same build is legible on a light
// terminal and a dark one: the dark variants are brighter, the light variants
// are darker, and both keep enough contrast against their background.
type Palette struct {
	styles  map[Severity]lipgloss.Style
	Muted   lipgloss.Style
	Header  lipgloss.Style
	Label   lipgloss.Style
	Value   lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Accent  lipgloss.Style
	Enabled bool
}

// NewPalette builds the color scheme. Pass enabled=false for plain text.
func NewPalette(enabled bool) *Palette {
	p := &Palette{Enabled: enabled, styles: map[Severity]lipgloss.Style{}}
	if !enabled {
		blank := lipgloss.NewStyle()
		for s := SevUnknown; s <= SevOver; s++ {
			p.styles[s] = blank
		}
		p.Muted, p.Header, p.Label, p.Value = blank, blank, blank, blank
		p.Warning, p.Error, p.Accent = blank, blank, blank
		return p
	}

	c := func(dark, light string) lipgloss.AdaptiveColor {
		return lipgloss.AdaptiveColor{Dark: dark, Light: light}
	}

	p.styles[SevUnknown] = lipgloss.NewStyle().Foreground(c("#6b7280", "#9ca3af"))
	p.styles[SevIdle] = lipgloss.NewStyle().Foreground(c("#4ade80", "#15803d"))
	p.styles[SevNormal] = lipgloss.NewStyle().Foreground(c("#22c55e", "#166534"))
	p.styles[SevElevated] = lipgloss.NewStyle().Foreground(c("#facc15", "#a16207"))
	p.styles[SevHigh] = lipgloss.NewStyle().Foreground(c("#fb923c", "#c2410c"))
	p.styles[SevCritical] = lipgloss.NewStyle().Foreground(c("#f87171", "#b91c1c"))
	p.styles[SevOver] = lipgloss.NewStyle().Foreground(c("#fca5a5", "#7f1d1d")).Bold(true)

	p.Muted = lipgloss.NewStyle().Foreground(c("#6b7280", "#9ca3af"))
	p.Header = lipgloss.NewStyle().Foreground(c("#e5e7eb", "#111827")).Bold(true)
	p.Label = lipgloss.NewStyle().Foreground(c("#d1d5db", "#374151"))
	p.Value = lipgloss.NewStyle().Foreground(c("#f9fafb", "#111827"))
	p.Warning = lipgloss.NewStyle().Foreground(c("#fbbf24", "#b45309"))
	p.Error = lipgloss.NewStyle().Foreground(c("#f87171", "#b91c1c")).Bold(true)
	p.Accent = lipgloss.NewStyle().Foreground(c("#60a5fa", "#1d4ed8"))
	return p
}

// For returns the style for a severity.
func (p *Palette) For(s Severity) lipgloss.Style { return p.styles[s] }

// Fraction returns the style for a usage fraction under the given thresholds.
func (p *Palette) Fraction(t Thresholds, f float64, ok bool) lipgloss.Style {
	return p.For(t.Classify(f, ok))
}

// ColorEnabled decides whether to emit ANSI color.
//
// It honors the NO_COLOR convention and the common CLICOLOR_FORCE escape
// hatch, then falls back to whether stdout is a terminal. Piping output into
// grep should not produce escape codes.
func ColorEnabled(force, disable bool) bool {
	if disable {
		return false
	}
	if force {
		return true
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// SeverityLabel names a band for the legend and for notification text.
func SeverityLabel(s Severity) string {
	switch s {
	case SevIdle:
		return "idle"
	case SevNormal:
		return "normal"
	case SevElevated:
		return "elevated"
	case SevHigh:
		return "high"
	case SevCritical:
		return "critical"
	case SevOver:
		return "over limit"
	}
	return "unknown"
}

// WorstSeverity returns the more severe of two bands, for rows that are hot on
// more than one metric.
func WorstSeverity(a, b Severity) Severity {
	if b > a {
		return b
	}
	return a
}

// RowSeverity classifies a row on its worst metric, which is what a
// single-color row indicator should reflect.
func RowSeverity(t Thresholds, u model.Usage) Severity {
	worst := SevUnknown
	for _, m := range []model.Metric{model.MetricCPU, model.MetricMemory, model.MetricStorage} {
		if f, _, ok := u.BestFraction(m); ok {
			worst = WorstSeverity(worst, t.Classify(f, true))
		}
	}
	return worst
}
