package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
	"github.com/mikeoertli/kube_resource_monitor/internal/render"
)

// View implements tea.Model.
func (m *Model) View() string {
	if !m.ready {
		return "starting…"
	}
	if m.showHelp {
		return m.helpView()
	}

	p := m.cfg.Palette
	var b strings.Builder

	b.WriteString(m.statusBar())
	b.WriteString("\n")

	if m.lastErr != nil {
		b.WriteString(p.Error.Render("error: " + m.lastErr.Error()))
		b.WriteString("\n")
	}
	if m.snapshot != nil {
		for _, w := range m.snapshot.Warnings {
			b.WriteString(p.Warning.Render("! " + truncate(w, m.width)))
			b.WriteString("\n")
		}
	}
	if banner := m.alertBanner(); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
	}

	header, lines := m.tbl.Lines(m.flat)
	b.WriteString(clip(header, m.width))
	b.WriteString("\n")

	vis := m.visibleRows()
	end := m.offset + vis
	if end > len(lines) {
		end = len(lines)
	}

	if len(lines) == 0 {
		b.WriteString(p.Muted.Render(m.emptyMessage()))
		b.WriteString("\n")
	}
	for i := m.offset; i < end; i++ {
		line := clip(lines[i], m.width)
		if i == m.cursor {
			// Reverse video for the cursor rather than a background color, so
			// the severity colors on the row stay readable.
			line = lipgloss.NewStyle().Reverse(true).Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	// Pad so the footer does not jump around as the row count changes.
	for i := end - m.offset; i < vis; i++ {
		b.WriteString("\n")
	}

	b.WriteString(m.footer())
	return b.String()
}

func (m *Model) emptyMessage() string {
	if m.loading && m.snapshot == nil {
		return "  collecting…"
	}
	if m.filter.Value() != "" {
		return fmt.Sprintf("  nothing matches %q — press esc to clear the filter", m.filter.Value())
	}
	if m.snapshot != nil && m.snapshot.MissingMetrics > 0 {
		return fmt.Sprintf("  no rows with metrics yet (%d pods have no sample; metrics-server needs a scrape interval)",
			m.snapshot.MissingMetrics)
	}
	return "  nothing to show here"
}

func (m *Model) statusBar() string {
	p := m.cfg.Palette

	ns := m.cfg.Options.Namespace
	if ns == "" {
		ns = "all namespaces"
	}

	state := p.For(render.SevIdle).Render("●")
	switch {
	case m.lastErr != nil:
		state = p.Error.Render("●")
	case m.paused:
		state = p.Warning.Render("‖")
	case m.loading:
		state = p.Accent.Render("◐")
	}

	dir := "↓"
	if !m.cfg.Descending {
		dir = "↑"
	}

	left := strings.Join([]string{
		state,
		p.Accent.Render("krm"),
		p.Label.Render(m.cfg.ContextName),
		p.Muted.Render("·"),
		p.Label.Render(ns),
	}, " ")

	right := strings.Join([]string{
		p.Muted.Render("group") + " " + p.Value.Render(string(m.cfg.Options.GroupBy)),
		p.Muted.Render("sort") + " " + p.Value.Render(string(m.cfg.Sort)+dir),
		p.Muted.Render("every") + " " + p.Value.Render(intervalLabel(m.cfg.Interval)),
		p.Muted.Render(m.freshness()),
	}, "  ")

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// freshness reports how stale the numbers are.
//
// This matters more than it sounds: metrics-server averages over a ~30s window
// and the UI refreshes on its own schedule, so a reading can legitimately be a
// minute behind reality. Showing the age stops people from chasing a spike that
// has already passed.
func (m *Model) freshness() string {
	if m.lastRefresh.IsZero() {
		return "never"
	}
	age := time.Since(m.lastRefresh)
	label := model.FormatAge(age) + " ago"
	if m.snapshot != nil && m.snapshot.Window > 0 {
		label += fmt.Sprintf(" (%s avg)", model.FormatAge(m.snapshot.Window))
	}
	if m.paused {
		label = "paused · " + label
	}
	return label
}

func (m *Model) alertBanner() string {
	if len(m.alerts) == 0 || time.Since(m.alertTime) > 30*time.Second {
		return ""
	}
	p := m.cfg.Palette
	firing := 0
	for _, a := range m.alerts {
		if a.Firing {
			firing++
		}
	}
	if firing == 0 {
		return p.For(render.SevNormal).Render(fmt.Sprintf("✓ %d alert(s) resolved", len(m.alerts)))
	}
	first := m.alerts[0]
	msg := fmt.Sprintf("▲ %s", first.Body())
	if firing > 1 {
		msg += fmt.Sprintf("  (+%d more)", firing-1)
	}
	return p.Error.Render(truncate(msg, m.width))
}

func (m *Model) footer() string {
	p := m.cfg.Palette

	if m.filtering {
		return p.Accent.Render("filter ") + m.filter.View()
	}

	var totals string
	if m.snapshot != nil {
		totals = m.tbl.TotalsLine(m.snapshot.Totals, len(m.flat))
	}

	pos := ""
	if len(m.flat) > 0 {
		pos = p.Muted.Render(fmt.Sprintf("  [%d/%d]", m.cursor+1, len(m.flat)))
	}

	hints := p.Muted.Render("?") + p.Muted.Render(" help  ") +
		p.Muted.Render("/ filter  t group  s sort  c containers  p pause  Q quit")

	line1 := clip(totals+pos, m.width)
	line2 := clip(m.tbl.Legend(), m.width)
	line3 := clip(hints, m.width)

	filterNote := ""
	if v := m.filter.Value(); v != "" {
		filterNote = p.Accent.Render("  filter:"+v) + p.Muted.Render(" (esc to clear)")
	}
	return line1 + filterNote + "\n" + line2 + "\n" + line3
}

func (m *Model) helpView() string {
	p := m.cfg.Palette
	var b strings.Builder

	b.WriteString(p.Header.Render("kube-resource-monitor — keys"))
	b.WriteString("\n\n")

	for _, g := range helpGroups {
		b.WriteString(p.Accent.Render(g.Title))
		b.WriteString("\n")
		for _, bind := range g.Bindings {
			h := bind.Help()
			b.WriteString("  " + p.Value.Render(pad(h.Key, 10)) + p.Muted.Render(h.Desc) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(p.Accent.Render("Color scale"))
	b.WriteString("\n  ")
	b.WriteString(m.tbl.Legend())
	b.WriteString("\n\n")
	b.WriteString(p.Muted.Render("  Color reflects usage against the limit where one is declared,\n"))
	b.WriteString(p.Muted.Render("  falling back to the request and then to node capacity.\n"))
	b.WriteString(p.Muted.Render("  A dotted bar means no denominator exists to measure against.\n\n"))
	b.WriteString(p.Muted.Render("press ? or h to return"))
	return b.String()
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}

// clip truncates a rendered line to the terminal width.
//
// lipgloss.Width is ANSI-aware, but naive byte slicing is not: cutting through
// an escape sequence would leak color codes into the rest of the screen. When
// a line is too long we fall back to truncating the plain text.
func clip(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return truncate(s, width)
}

func truncate(s string, width int) string {
	if width <= 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	// Walk runes, tracking display width and copying escape sequences verbatim.
	var b strings.Builder
	w := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
		}
		if inEscape {
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteString("…")
	// Reset in case we cut inside a styled run.
	b.WriteString("\x1b[0m")
	return b.String()
}
