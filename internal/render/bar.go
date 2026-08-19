package render

import (
	"strings"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// BarStyle selects the glyph set used to draw meters.
type BarStyle string

const (
	// BarBlocks uses Unicode eighth-blocks for sub-character resolution, which
	// matters a lot at the narrow widths a table column allows.
	BarBlocks BarStyle = "blocks"
	// BarASCII is the fallback for terminals and fonts that mangle box drawing.
	BarASCII BarStyle = "ascii"
	// BarBraille packs the most resolution into the fewest columns.
	BarBraille BarStyle = "braille"
)

var eighths = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// Bar renders a horizontal meter of the given width.
//
// Sub-character precision is deliberate: at width 10 a whole-block bar can only
// express 10% steps, so 84% and 89% look identical even though one is fine and
// one is about to be throttled. Eighth-blocks give 80 distinguishable levels in
// the same space.
//
// Values above 100% are clamped for drawing but the caller still shows the true
// percentage alongside, so nothing is hidden -- a full bar plus "140%" reads
// correctly, whereas a bar that overflows its column does not.
func Bar(fraction float64, ok bool, width int, style BarStyle) string {
	if width <= 0 {
		return ""
	}
	if !ok {
		// A dotted track communicates "no denominator" rather than "zero",
		// which an empty bar would wrongly imply.
		return strings.Repeat("·", width)
	}
	f := model.ClampFraction(fraction)

	switch style {
	case BarASCII:
		filled := int(f*float64(width) + 0.5)
		return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	case BarBraille:
		return brailleBar(f, width)
	default:
		total := f * float64(width)
		full := int(total)
		rem := total - float64(full)
		if full > width {
			full = width
		}
		var b strings.Builder
		b.WriteString(strings.Repeat("█", full))
		remaining := width - full
		if remaining > 0 {
			idx := int(rem * 8)
			if idx > 7 {
				idx = 7
			}
			if idx > 0 {
				b.WriteRune(eighths[idx])
				remaining--
			}
			b.WriteString(strings.Repeat("░", remaining))
		}
		return b.String()
	}
}

// brailleBar packs two horizontal dots per cell.
func brailleBar(f float64, width int) string {
	cells := width * 2
	on := int(f*float64(cells) + 0.5)
	var b strings.Builder
	for i := 0; i < width; i++ {
		left := on > i*2
		right := on > i*2+1
		switch {
		case left && right:
			b.WriteRune('⣿')
		case left:
			b.WriteRune('⡇')
		default:
			b.WriteRune('⠄')
		}
	}
	return b.String()
}

// Sparkline renders a series as a single line of varying-height blocks, for the
// TUI's per-row history.
//
// Scaling is against the caller-supplied max rather than the series max, so a
// row's sparkline stays comparable with itself over time instead of silently
// re-normalizing every tick.
func Sparkline(values []float64, max float64) string {
	if len(values) == 0 {
		return ""
	}
	levels := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	if max <= 0 {
		for _, v := range values {
			if v > max {
				max = v
			}
		}
	}
	if max <= 0 {
		return strings.Repeat("▁", len(values))
	}
	var b strings.Builder
	for _, v := range values {
		idx := int(model.ClampFraction(v/max) * float64(len(levels)-1))
		b.WriteRune(levels[idx])
	}
	return b.String()
}
