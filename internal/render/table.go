package render

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Options controls which columns appear and how they are drawn.
type Options struct {
	ShowNamespace bool
	ShowRequests  bool
	ShowLimits    bool
	ShowBars      bool
	ShowNode      bool
	ShowAge       bool
	ShowRestarts  bool
	ShowReady     bool
	ShowLabels    bool
	// Storage swaps the CPU/memory columns for storage ones, for volume views.
	Storage bool

	BarWidth   int
	BarStyle   BarStyle
	Thresholds Thresholds
	// Basis picks which denominator the percentage column reports. Empty means
	// "the best available", preferring limits.
	Basis model.Basis
}

// DefaultOptions is a sensible starting column set.
func DefaultOptions() Options {
	return Options{
		ShowReady:  true,
		ShowBars:   true,
		BarWidth:   12,
		BarStyle:   BarBlocks,
		Thresholds: DefaultThresholds,
	}
}

type align int

const (
	alignLeft align = iota
	alignRight
)

type column struct {
	title string
	align align
}

type cell struct {
	text  string
	style lipgloss.Style
}

// Table lays rows out into aligned, colored text.
type Table struct {
	Palette *Palette
	Opts    Options
}

// NewTable builds a renderer.
func NewTable(p *Palette, o Options) *Table {
	if o.BarWidth <= 0 {
		o.BarWidth = 12
	}
	if o.BarStyle == "" {
		o.BarStyle = BarBlocks
	}
	if o.Thresholds == (Thresholds{}) {
		o.Thresholds = DefaultThresholds
	}
	return &Table{Palette: p, Opts: o}
}

func (t *Table) columns() []column {
	cols := []column{{"NAME", alignLeft}, {"KIND", alignLeft}}
	if t.Opts.ShowNamespace {
		cols = append([]column{{"NAMESPACE", alignLeft}}, cols...)
	}
	if t.Opts.ShowReady {
		cols = append(cols, column{"READY", alignRight})
	}

	if t.Opts.Storage {
		cols = append(cols, column{"USED", alignRight}, column{"SIZE", alignRight}, column{"USE%", alignRight})
		if t.Opts.ShowBars {
			cols = append(cols, column{"STORAGE", alignLeft})
		}
	} else {
		cols = append(cols, column{"CPU", alignRight})
		if t.Opts.ShowRequests {
			cols = append(cols, column{"CPU REQ", alignRight})
		}
		if t.Opts.ShowLimits {
			cols = append(cols, column{"CPU LIM", alignRight})
		}
		cols = append(cols, column{"CPU%", alignRight})
		if t.Opts.ShowBars {
			cols = append(cols, column{"CPU USE", alignLeft})
		}

		cols = append(cols, column{"MEM", alignRight})
		if t.Opts.ShowRequests {
			cols = append(cols, column{"MEM REQ", alignRight})
		}
		if t.Opts.ShowLimits {
			cols = append(cols, column{"MEM LIM", alignRight})
		}
		cols = append(cols, column{"MEM%", alignRight})
		if t.Opts.ShowBars {
			cols = append(cols, column{"MEM USE", alignLeft})
		}
	}

	if t.Opts.ShowRestarts {
		cols = append(cols, column{"RESTARTS", alignRight})
	}
	if t.Opts.ShowNode {
		cols = append(cols, column{"NODE", alignLeft})
	}
	if t.Opts.ShowAge {
		cols = append(cols, column{"AGE", alignRight})
	}
	if t.Opts.ShowLabels {
		cols = append(cols, column{"LABELS", alignLeft})
	}
	return cols
}

// fractionFor picks the percentage a column should show, honoring an explicit
// basis when one was requested and falling back to the best available.
func (t *Table) fractionFor(u model.Usage, m model.Metric) (float64, bool) {
	if t.Opts.Basis != "" {
		return u.Fraction(m, t.Opts.Basis)
	}
	f, _, ok := u.BestFraction(m)
	return f, ok
}

func (t *Table) rowCells(fr model.FlatRow) []cell {
	r := fr.Row
	p := t.Palette

	// Indent nested rows with a tree connector so a pod under a Deployment
	// reads as belonging to it even after sorting reorders everything.
	name := r.Name
	if fr.Depth > 0 {
		name = strings.Repeat("  ", fr.Depth-1) + "└ " + name
	} else if len(r.Children) > 0 {
		name = "▸ " + name
	} else {
		name = "  " + name
	}

	out := []cell{}
	if t.Opts.ShowNamespace {
		out = append(out, cell{r.Namespace, p.Muted})
	}
	out = append(out,
		cell{name, p.Value},
		cell{r.Kind.Short(), p.Muted},
	)
	if t.Opts.ShowReady {
		out = append(out, cell{orDash(r.Ready), p.Muted})
	}

	dash := cell{"-", p.Muted}

	if t.Opts.Storage {
		if r.MetricsMissing {
			out = append(out, dash)
		} else {
			f, ok := r.Usage.StorageOfCapacity()
			out = append(out, cell{model.FormatBytes(r.Usage.Used.StorageBytes), p.Fraction(t.Opts.Thresholds, f, ok)})
		}
		out = append(out, cell{model.FormatBytes(r.Usage.Capacity.StorageBytes), p.Muted})
		f, ok := r.Usage.StorageOfCapacity()
		if r.MetricsMissing {
			ok = false
		}
		out = append(out, cell{model.FormatPercent(f, ok), p.Fraction(t.Opts.Thresholds, f, ok)})
		if t.Opts.ShowBars {
			out = append(out, cell{Bar(f, ok, t.Opts.BarWidth, t.Opts.BarStyle), p.Fraction(t.Opts.Thresholds, f, ok)})
		}
	} else {
		for _, m := range []model.Metric{model.MetricCPU, model.MetricMemory} {
			f, ok := t.fractionFor(r.Usage, m)
			style := p.Fraction(t.Opts.Thresholds, f, ok)

			if r.MetricsMissing {
				out = append(out, dash)
			} else {
				out = append(out, cell{model.FormatMetric(m, r.Usage.Value(m)), style})
			}
			if t.Opts.ShowRequests {
				out = append(out, cell{declaredCell(r.Usage, m, model.BasisRequest), p.Muted})
			}
			if t.Opts.ShowLimits {
				out = append(out, cell{declaredCell(r.Usage, m, model.BasisLimit), p.Muted})
			}
			showPct := ok && !r.MetricsMissing
			out = append(out, cell{model.FormatPercent(f, showPct), style})
			if t.Opts.ShowBars {
				out = append(out, cell{Bar(f, showPct, t.Opts.BarWidth, t.Opts.BarStyle), style})
			}
		}
	}

	if t.Opts.ShowRestarts {
		style := p.Muted
		if r.Restarts > 0 {
			style = p.Warning
		}
		out = append(out, cell{itoa(int(r.Restarts)), style})
	}
	if t.Opts.ShowNode {
		out = append(out, cell{orDash(r.Node), p.Muted})
	}
	if t.Opts.ShowAge {
		out = append(out, cell{model.FormatAge(r.Age), p.Muted})
	}
	if t.Opts.ShowLabels {
		out = append(out, cell{model.FormatLabels(r.Labels), p.Muted})
	}
	return out
}

// declaredCell renders a request or limit, distinguishing "not set" from zero.
func declaredCell(u model.Usage, m model.Metric, b model.Basis) string {
	var set bool
	var v int64
	switch {
	case m == model.MetricCPU && b == model.BasisRequest:
		set, v = u.HasCPURequest, u.Requests.CPUMilli
	case m == model.MetricCPU && b == model.BasisLimit:
		set, v = u.HasCPULimit, u.Limits.CPUMilli
	case m == model.MetricMemory && b == model.BasisRequest:
		set, v = u.HasMemRequest, u.Requests.MemBytes
	case m == model.MetricMemory && b == model.BasisLimit:
		set, v = u.HasMemLimit, u.Limits.MemBytes
	}
	if !set {
		return "-"
	}
	return model.FormatMetric(m, v)
}

// Lines renders the header and one string per row, pre-padded to a shared
// column layout.
//
// Header and rows are returned separately so the TUI can pin the header while
// scrolling the body, and so it can apply a cursor highlight to a single line
// without re-laying out the table.
func (t *Table) Lines(flat []model.FlatRow) (header string, rows []string) {
	cols := t.columns()
	grid := make([][]cell, 0, len(flat))
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c.title)
	}

	for _, fr := range flat {
		cells := t.rowCells(fr)
		for i := range cells {
			if i < len(widths) {
				if w := lipgloss.Width(cells[i].text); w > widths[i] {
					widths[i] = w
				}
			}
		}
		grid = append(grid, cells)
	}

	var hb strings.Builder
	for i, c := range cols {
		if i > 0 {
			hb.WriteString("  ")
		}
		hb.WriteString(t.Palette.Header.Render(pad(c.title, widths[i], c.align)))
	}
	header = hb.String()

	rows = make([]string, 0, len(grid))
	for _, cells := range grid {
		var b strings.Builder
		for i := range cols {
			if i > 0 {
				b.WriteString("  ")
			}
			text := ""
			var style lipgloss.Style
			if i < len(cells) {
				text, style = cells[i].text, cells[i].style
			}
			b.WriteString(style.Render(pad(text, widths[i], cols[i].align)))
		}
		rows = append(rows, b.String())
	}
	return header, rows
}

// Render produces the whole table as one string.
func (t *Table) Render(flat []model.FlatRow) string {
	header, rows := t.Lines(flat)
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

// TotalsLine summarizes a set of rows for the table footer.
func (t *Table) TotalsLine(u model.Usage, rowCount int) string {
	p := t.Palette
	parts := []string{p.Label.Render("TOTAL"), p.Muted.Render("(" + itoa(rowCount) + " rows)")}
	if t.Opts.Storage {
		f, ok := u.StorageOfCapacity()
		parts = append(parts,
			p.Value.Render(model.FormatBytes(u.Used.StorageBytes)+" used"),
			p.Muted.Render("of "+model.FormatBytes(u.Capacity.StorageBytes)),
			p.Fraction(t.Opts.Thresholds, f, ok).Render(model.FormatPercent(f, ok)),
		)
	} else {
		cf, cok := t.fractionFor(u, model.MetricCPU)
		mf, mok := t.fractionFor(u, model.MetricMemory)
		cpuDen, memDen := declaredCell(u, model.MetricCPU, model.BasisLimit), declaredCell(u, model.MetricMemory, model.BasisLimit)
		denLabel := "limit"
		if u.PreferCapacity {
			// Node and volume views measure against capacity, so the footer
			// must not quietly switch denominators on the reader.
			cpuDen, memDen = model.FormatCPU(u.Capacity.CPUMilli), model.FormatBytes(u.Capacity.MemBytes)
			denLabel = "allocatable"
		}
		parts = append(parts,
			p.Label.Render("cpu")+" "+p.Fraction(t.Opts.Thresholds, cf, cok).Render(model.FormatCPU(u.Used.CPUMilli)),
			p.Muted.Render("/ "+cpuDen),
			p.Label.Render("mem")+" "+p.Fraction(t.Opts.Thresholds, mf, mok).Render(model.FormatBytes(u.Used.MemBytes)),
			p.Muted.Render("/ "+memDen),
			p.Muted.Render("("+denLabel+")"),
		)
	}
	return strings.Join(parts, "  ")
}

// Legend explains the color bands, so the scale is discoverable without docs.
func (t *Table) Legend() string {
	p := t.Palette
	th := t.Opts.Thresholds
	segs := []struct {
		sev   Severity
		label string
	}{
		{SevIdle, "<" + pct(th.Normal)},
		{SevNormal, pct(th.Normal) + "+"},
		{SevElevated, pct(th.Elevated) + "+"},
		{SevHigh, pct(th.High) + "+"},
		{SevCritical, pct(th.Critical) + "+"},
		{SevOver, "over"},
		{SevUnknown, "no limit"},
	}
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, p.For(s.sev).Render("██ "+s.label))
	}
	return strings.Join(parts, "  ")
}

func pct(f float64) string {
	return model.FormatPercent(f, true)
}

func pad(s string, w int, a align) string {
	gap := w - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	if a == alignRight {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
