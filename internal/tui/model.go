// Package tui implements the interactive watch view.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikeoertli/kube_resource_monitor/internal/inventory"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
	"github.com/mikeoertli/kube_resource_monitor/internal/notify"
	"github.com/mikeoertli/kube_resource_monitor/internal/render"
)

// Collector is the subset of the inventory collector the UI needs, expressed as
// an interface so the view can be driven by a stub in tests.
type Collector interface {
	Collect(ctx context.Context, opts inventory.Options) (*inventory.Snapshot, error)
}

// Config configures the UI.
type Config struct {
	Collector Collector
	Options   inventory.Options
	Render    render.Options
	Palette   *render.Palette

	Interval time.Duration
	// ContextName and Namespace are shown in the status bar.
	ContextName string
	Namespace   string
	Source      string

	Sort       model.SortKey
	Descending bool

	// Watcher, when set, evaluates threshold rules each refresh and surfaces a
	// banner. Notification delivery stays outside the UI.
	Watcher  *notify.Watcher
	Notifier notify.Notifier
}

// Model is the Bubble Tea model.
type Model struct {
	cfg Config

	snapshot *inventory.Snapshot
	flat     []model.FlatRow
	expanded map[string]bool

	cursor  int
	offset  int
	width   int
	height  int
	ready   bool
	paused  bool
	loading bool

	filter    textinput.Model
	filtering bool
	// started is when the view opened, used to show a transient orientation
	// hint. Someone who typed a bare `krm` may not realize they have entered a
	// live, full-screen view at all.
	started     time.Time
	showHelp    bool
	lastErr     error
	lastRefresh time.Time
	alerts      []notify.Alert
	alertTime   time.Time

	tbl *render.Table
	// generation guards against a slow in-flight collection landing after the
	// user has already changed the query it was answering.
	generation int
}

// New builds the model.
func New(cfg Config) *Model {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.Sort == "" {
		cfg.Sort = model.SortCPU
	}
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "name or regex"
	ti.CharLimit = 128

	m := &Model{
		cfg:      cfg,
		expanded: map[string]bool{},
		filter:   ti,
		tbl:      render.NewTable(cfg.Palette, cfg.Render),
		started:  time.Now(),
	}
	m.filter.SetValue(cfg.Options.NamePattern)
	return m
}

type snapshotMsg struct {
	snap       *inventory.Snapshot
	err        error
	generation int
}

type tickMsg time.Time

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.collect(), tick(m.cfg.Interval))
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// collect runs a collection off the UI goroutine.
//
// The timeout is derived from the refresh interval so a cluster slower than the
// refresh rate degrades into "one request at a time" rather than piling up
// overlapping requests until the apiserver rate-limits us.
func (m *Model) collect() tea.Cmd {
	gen := m.generation
	opts := m.cfg.Options
	opts.NamePattern = m.filter.Value()
	timeout := m.cfg.Interval * 3
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	coll := m.cfg.Collector

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		snap, err := coll.Collect(ctx, opts)
		return snapshotMsg{snap: snap, err: err, generation: gen}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.autoColumns()
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tick(m.cfg.Interval))
		if !m.paused && !m.loading {
			m.loading = true
			cmds = append(cmds, m.collect())
		}
		return m, tea.Batch(cmds...)

	case snapshotMsg:
		m.loading = false
		if msg.generation != m.generation {
			// Stale result for a query the user has since changed.
			return m, nil
		}
		if msg.err != nil {
			m.lastErr = msg.err
			return m, nil
		}
		m.lastErr = nil
		m.snapshot = msg.snap
		m.lastRefresh = time.Now()
		m.rebuild()
		return m, m.evaluateAlerts()

	case alertsMsg:
		m.alerts = msg.alerts
		m.alertTime = time.Now()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

type alertsMsg struct{ alerts []notify.Alert }

// evaluateAlerts runs the threshold rules and delivers notifications.
//
// Delivery happens in a command rather than inline because a notifier shells
// out to osascript or notify-send, and a 200ms process spawn on the UI
// goroutine would visibly stutter the refresh.
func (m *Model) evaluateAlerts() tea.Cmd {
	if m.cfg.Watcher == nil || m.snapshot == nil {
		return nil
	}
	alerts := m.cfg.Watcher.Evaluate(m.snapshot.Rows)
	if len(alerts) == 0 {
		return nil
	}
	notifier := m.cfg.Notifier
	return func() tea.Msg {
		if notifier != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, a := range alerts {
				_ = notifier.Notify(ctx, a)
			}
		}
		return alertsMsg{alerts: alerts}
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the filter box has focus, almost every key is text input; only
	// enter and escape are commands. Anything else would make typing "cpu"
	// toggle columns.
	if m.filtering {
		switch msg.String() {
		case "enter":
			m.filtering = false
			m.filter.Blur()
			m.generation++
			m.loading = true
			return m, m.collect()
		case "esc":
			m.filtering = false
			m.filter.Blur()
			m.filter.SetValue("")
			m.generation++
			m.loading = true
			return m, m.collect()
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit

	case msg.String() == "q":
		// Lowercase q toggles the requests column; quitting is Q or ctrl+c.
		// In a view where you are constantly toggling columns, having q drop
		// you out of the program would be a trap.
		m.cfg.Render.ShowRequests = !m.cfg.Render.ShowRequests
		m.rebuildTable()
		return m, nil

	case key.Matches(msg, keys.Help):
		m.showHelp = !m.showHelp
		return m, nil

	case key.Matches(msg, keys.Up):
		m.moveCursor(-1)
	case key.Matches(msg, keys.Down):
		m.moveCursor(1)
	case key.Matches(msg, keys.PageUp):
		m.moveCursor(-m.visibleRows())
	case key.Matches(msg, keys.PageDown):
		m.moveCursor(m.visibleRows())
	case key.Matches(msg, keys.Home):
		m.cursor = 0
		m.clampScroll()
	case key.Matches(msg, keys.End):
		m.cursor = len(m.flat) - 1
		m.clampScroll()

	case key.Matches(msg, keys.Expand):
		if m.cursor >= 0 && m.cursor < len(m.flat) {
			fr := m.flat[m.cursor]
			if len(fr.Row.Children) > 0 {
				m.expanded[fr.Key] = !m.expanded[fr.Key]
				m.rebuild()
			}
		}
	case key.Matches(msg, keys.ExpandAll):
		m.setAllExpanded(true)
	case key.Matches(msg, keys.CollapseAll):
		m.setAllExpanded(false)

	case key.Matches(msg, keys.Sort):
		m.cycleSort()
		m.rebuild()
	case key.Matches(msg, keys.SortReverse):
		m.cfg.Descending = !m.cfg.Descending
		m.rebuild()

	case key.Matches(msg, keys.Group):
		m.cycleGroup()
		m.generation++
		m.loading = true
		return m, m.collect()

	case key.Matches(msg, keys.Filter):
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink

	case key.Matches(msg, keys.ClearFilter):
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.generation++
			m.loading = true
			return m, m.collect()
		}

	case key.Matches(msg, keys.Containers):
		m.cfg.Options.IncludeContainers = !m.cfg.Options.IncludeContainers
		m.generation++
		m.loading = true
		return m, m.collect()

	case key.Matches(msg, keys.Limits):
		m.cfg.Render.ShowLimits = !m.cfg.Render.ShowLimits
		m.rebuildTable()
	case key.Matches(msg, keys.Bars):
		m.cfg.Render.ShowBars = !m.cfg.Render.ShowBars
		m.rebuildTable()

	case key.Matches(msg, keys.OnlyProblem):
		m.cfg.Options.OnlyProblems = !m.cfg.Options.OnlyProblems
		m.generation++
		m.loading = true
		return m, m.collect()

	case key.Matches(msg, keys.Namespaces):
		if m.cfg.Options.Namespace == "" {
			m.cfg.Options.Namespace = m.cfg.Namespace
		} else {
			m.cfg.Options.Namespace = ""
		}
		m.cfg.Render.ShowNamespace = m.cfg.Options.Namespace == ""
		m.rebuildTable()
		m.generation++
		m.loading = true
		return m, m.collect()

	case key.Matches(msg, keys.Pause):
		m.paused = !m.paused
	case key.Matches(msg, keys.Refresh):
		if !m.loading {
			m.loading = true
			return m, m.collect()
		}
	case key.Matches(msg, keys.Faster):
		m.cfg.Interval = adjustInterval(m.cfg.Interval, -1)
	case key.Matches(msg, keys.Slower):
		m.cfg.Interval = adjustInterval(m.cfg.Interval, 1)
	}
	return m, nil
}

// adjustInterval steps through a set of sensible refresh rates rather than
// adding a fixed delta, so one keypress meaningfully changes the cadence at
// both 1s and 5m.
func adjustInterval(cur time.Duration, dir int) time.Duration {
	steps := []time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
		15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	}
	idx := 0
	for i, s := range steps {
		if s >= cur {
			idx = i
			break
		}
		idx = len(steps) - 1
	}
	idx += dir
	if idx < 0 {
		idx = 0
	}
	if idx >= len(steps) {
		idx = len(steps) - 1
	}
	return steps[idx]
}

func (m *Model) cycleSort() {
	cur := m.cfg.Sort
	for i, k := range model.AllSortKeys {
		if k == cur {
			m.cfg.Sort = model.AllSortKeys[(i+1)%len(model.AllSortKeys)]
			return
		}
	}
	m.cfg.Sort = model.AllSortKeys[0]
}

func (m *Model) cycleGroup() {
	cur := m.cfg.Options.GroupBy
	for i, g := range inventory.AllGroupBy {
		if g == cur {
			m.cfg.Options.GroupBy = inventory.AllGroupBy[(i+1)%len(inventory.AllGroupBy)]
			m.afterGroupChange()
			return
		}
	}
	m.cfg.Options.GroupBy = inventory.AllGroupBy[0]
	m.afterGroupChange()
}

// afterGroupChange adjusts the columns that only make sense for some groupings.
func (m *Model) afterGroupChange() {
	g := m.cfg.Options.GroupBy
	m.cfg.Render.Storage = g == inventory.GroupPVC
	m.cfg.Render.ShowNode = g == inventory.GroupPod || g == inventory.GroupContainer
	if g == inventory.GroupContainer {
		m.cfg.Options.IncludeContainers = true
	}
	// Sorting by CPU in a volume view would leave every row at zero.
	if m.cfg.Render.Storage && (m.cfg.Sort == model.SortCPU || m.cfg.Sort == model.SortMemory) {
		m.cfg.Sort = model.SortStorage
	}
	m.expanded = map[string]bool{}
	m.cursor = 0
	m.offset = 0
	m.rebuildTable()
}

// autoColumns adapts the column set to the terminal width, dropping the widest
// optional columns first so a narrow window still shows usable numbers instead
// of wrapping into unreadable soup.
func (m *Model) autoColumns() {
	switch {
	case m.width < 80:
		m.cfg.Render.ShowBars = false
		m.cfg.Render.ShowRequests = false
		m.cfg.Render.ShowLimits = false
		m.cfg.Render.BarWidth = 6
	case m.width < 110:
		m.cfg.Render.BarWidth = 8
	case m.width < 150:
		m.cfg.Render.BarWidth = 12
	default:
		m.cfg.Render.BarWidth = 16
	}
	m.rebuildTable()
}

func (m *Model) rebuildTable() {
	m.tbl = render.NewTable(m.cfg.Palette, m.cfg.Render)
}

func (m *Model) rebuild() {
	if m.snapshot == nil {
		return
	}
	model.Sort(m.snapshot.Rows, m.cfg.Sort, m.cfg.Descending)
	m.flat = model.Flatten(m.snapshot.Rows, func(k string) bool { return m.expanded[k] })
	m.clampScroll()
}

func (m *Model) setAllExpanded(v bool) {
	if m.snapshot == nil {
		return
	}
	all := model.Flatten(m.snapshot.Rows, func(string) bool { return true })
	if !v {
		m.expanded = map[string]bool{}
	} else {
		for _, fr := range all {
			if len(fr.Row.Children) > 0 {
				m.expanded[fr.Key] = true
			}
		}
	}
	m.rebuild()
}

func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.flat) {
		m.cursor = len(m.flat) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.clampScroll()
}

func (m *Model) visibleRows() int {
	// header + status + filter/legend + footer
	chrome := 6
	n := m.height - chrome
	if n < 1 {
		n = 1
	}
	return n
}

func (m *Model) clampScroll() {
	vis := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	maxOffset := len(m.flat) - vis
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
}

// Run starts the program.
func Run(cfg Config) error {
	m := New(cfg)
	m.afterGroupChange()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// intervalLabel renders the refresh cadence compactly.
func intervalLabel(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return strings.TrimSuffix(d.String(), "0s")
}
