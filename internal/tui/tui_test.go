package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikeoertli/kube_resource_monitor/internal/inventory"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
	"github.com/mikeoertli/kube_resource_monitor/internal/render"
)

// stubCollector returns a canned snapshot and records how it was called.
type stubCollector struct {
	mu    sync.Mutex
	calls []inventory.Options
	snap  *inventory.Snapshot
	err   error
}

func (s *stubCollector) Collect(_ context.Context, opts inventory.Options) (*inventory.Snapshot, error) {
	s.mu.Lock()
	s.calls = append(s.calls, opts)
	s.mu.Unlock()
	return s.snap, s.err
}

func (s *stubCollector) lastCall() inventory.Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return inventory.Options{}
	}
	return s.calls[len(s.calls)-1]
}

func usage(cpu, cpuLim, mem, memLim int64) model.Usage {
	return model.Usage{
		Used:        model.Amounts{CPUMilli: cpu, MemBytes: mem},
		Limits:      model.Amounts{CPUMilli: cpuLim, MemBytes: memLim},
		HasCPULimit: true, HasMemLimit: true, UsedKnown: true,
	}
}

func testSnapshot() *inventory.Snapshot {
	rows := []*model.Row{
		{Kind: model.KindDeployment, Name: "web", Namespace: "prod", Ready: "2/2",
			Usage: usage(550, 1000, 500<<20, 1<<30),
			Children: []*model.Row{
				{Kind: model.KindPod, Name: "web-1", Namespace: "prod", Usage: usage(300, 500, 300<<20, 512<<20)},
				{Kind: model.KindPod, Name: "web-2", Namespace: "prod", Usage: usage(250, 500, 200<<20, 512<<20)},
			}},
		{Kind: model.KindStatefulSet, Name: "db", Namespace: "prod", Ready: "1/1",
			Usage: usage(1500, 2000, 3<<30, 4<<30)},
	}
	return &inventory.Snapshot{
		Rows:    rows,
		Totals:  model.TotalOf(rows),
		Taken:   time.Now(),
		GroupBy: inventory.GroupWorkload,
		Window:  30 * time.Second,
	}
}

func newTestModel(t *testing.T, stub *stubCollector) *Model {
	t.Helper()
	m := New(Config{
		Collector:   stub,
		Options:     inventory.Options{Namespace: "prod", GroupBy: inventory.GroupWorkload},
		Render:      render.DefaultOptions(),
		Palette:     render.NewPalette(false),
		Interval:    5 * time.Second,
		ContextName: "test-ctx",
		Namespace:   "prod",
		Sort:        model.SortCPU,
		Descending:  true,
	})
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	return m
}

// deliver runs a command and feeds its message back into the model, following
// any command that results, which is what the Bubble Tea runtime does.
//
// Tick messages are deliberately not followed. Update answers a tick by
// scheduling the next one, so following them would spin forever waiting out
// real refresh intervals. The step cap is a second belt-and-braces guard.
func deliver(m *Model, cmd tea.Cmd) {
	for steps := 0; cmd != nil && steps < 16; steps++ {
		msg := cmd()
		if msg == nil {
			return
		}
		if _, isTick := msg.(tickMsg); isTick {
			return
		}
		_, cmd = m.Update(msg)
	}
}

func TestModelRendersSnapshot(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)

	deliver(m, m.collect())

	out := m.View()
	for _, want := range []string{"web", "db", "test-ctx", "prod", "CPU", "MEM"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
	// Children are collapsed by default; a two-pod Deployment shows as one row.
	if strings.Contains(out, "web-1") {
		t.Error("children should be collapsed until expanded")
	}
}

func TestExpandRevealsChildren(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	// Cursor starts on the highest-CPU row (db), so move to web first.
	for i, fr := range m.flat {
		if fr.Row.Name == "web" {
			m.cursor = i
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	out := m.View()
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "web-2") {
		t.Errorf("expanding should reveal the pods:\n%s", out)
	}
}

// Expansion is keyed by row identity so it survives the tree being rebuilt on
// every refresh; otherwise a drilled-down view would snap shut each tick.
func TestExpansionSurvivesRefresh(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	for i, fr := range m.flat {
		if fr.Row.Name == "web" {
			m.cursor = i
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// A refresh returns a structurally identical but distinct snapshot.
	stub.snap = testSnapshot()
	deliver(m, m.collect())

	if !strings.Contains(m.View(), "web-1") {
		t.Errorf("expansion state was lost across a refresh:\n%s", m.View())
	}
}

func TestSortCycleChangesOrder(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	if m.flat[0].Row.Name != "db" {
		t.Fatalf("expected db first by CPU, got %q", m.flat[0].Row.Name)
	}
	before := m.cfg.Sort
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.cfg.Sort == before {
		t.Error("pressing s should advance the sort key")
	}
}

func TestReverseSortFlipsOrder(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	first := m.flat[0].Row.Name
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.flat[0].Row.Name == first {
		t.Errorf("reversing should change which row is first (still %q)", first)
	}
}

// Lowercase q toggles a column. Quitting is Q or ctrl+c, because in a view
// where you constantly toggle columns, q-to-quit would be a trap.
func TestLowercaseQTogglesRequestsAndDoesNotQuit(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	before := m.cfg.Render.ShowRequests
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.cfg.Render.ShowRequests == before {
		t.Error("q should toggle the requests column")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("lowercase q must not quit")
		}
	}

	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
	if quitCmd == nil {
		t.Fatal("Q should quit")
	}
	if _, isQuit := quitCmd().(tea.QuitMsg); !isQuit {
		t.Error("Q should produce a quit message")
	}
}

// While the filter box has focus, letters are text, not commands.
func TestFilterInputCapturesKeystrokes(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	sortBefore := m.cfg.Sort
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "web" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if m.cfg.Sort != sortBefore {
		t.Error("typing in the filter box must not trigger the sort binding")
	}
	if got := m.filter.Value(); got != "web" {
		t.Errorf("filter value = %q, want %q", got, "web")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	deliver(m, cmd)
	if got := stub.lastCall().NamePattern; got != "web" {
		t.Errorf("filter was not passed to the collector: got %q", got)
	}
}

func TestEscapeClearsFilter(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	deliver(m, cmd)

	if m.filter.Value() != "" {
		t.Errorf("escape should clear the filter, got %q", m.filter.Value())
	}
	if got := stub.lastCall().NamePattern; got != "" {
		t.Errorf("collector should have been re-run without a filter, got %q", got)
	}
}

func TestPauseStopsTickCollection(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	before := len(stub.calls)

	// A paused tick should reschedule itself and nothing else. Following the
	// returned command would just wait out a real refresh interval.
	m.Update(tickMsg(time.Now()))

	if len(stub.calls) != before {
		t.Errorf("a paused view should not collect on tick (calls %d -> %d)", before, len(stub.calls))
	}
	if !strings.Contains(m.View(), "paused") {
		t.Error("the status bar should say it is paused")
	}
}

// A slow collection answering a query the user has since changed must not
// overwrite the newer results.
func TestStaleSnapshotIsDiscarded(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	stale := snapshotMsg{snap: &inventory.Snapshot{}, generation: m.generation - 1}
	rowsBefore := len(m.flat)
	m.Update(stale)

	if len(m.flat) != rowsBefore {
		t.Errorf("a stale snapshot replaced current data (%d -> %d rows)", rowsBefore, len(m.flat))
	}
}

func TestCollectErrorIsSurfacedNotFatal(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	stub.err = context.DeadlineExceeded
	deliver(m, m.collect())

	out := m.View()
	if !strings.Contains(out, "error:") {
		t.Errorf("the error should be shown in the view:\n%s", out)
	}
	// The previous good data must still be on screen; blanking the table on a
	// transient timeout would be worse than showing slightly stale numbers.
	if !strings.Contains(out, "web") {
		t.Error("previous rows should survive a failed refresh")
	}
}

func TestGroupCycleRecollects(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	before := m.cfg.Options.GroupBy
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	deliver(m, cmd)

	if m.cfg.Options.GroupBy == before {
		t.Fatal("pressing t should change the grouping")
	}
	if stub.lastCall().GroupBy != m.cfg.Options.GroupBy {
		t.Errorf("collector was called with %q, want %q", stub.lastCall().GroupBy, m.cfg.Options.GroupBy)
	}
}

// Switching to the volume view must not leave the table sorted by a column
// that is always zero there.
func TestSwitchingToVolumeViewFixesSort(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	m.cfg.Options.GroupBy = inventory.GroupNamespace
	m.cfg.Sort = model.SortCPU

	for i := 0; i < len(inventory.AllGroupBy)+1; i++ {
		if m.cfg.Options.GroupBy == inventory.GroupPVC {
			break
		}
		m.cycleGroup()
	}
	if m.cfg.Options.GroupBy != inventory.GroupPVC {
		t.Fatal("never reached the pvc view")
	}
	if m.cfg.Sort != model.SortStorage {
		t.Errorf("sort = %q, want storage in the volume view", m.cfg.Sort)
	}
	if !m.cfg.Render.Storage {
		t.Error("the volume view should use storage columns")
	}
}

// A narrow terminal should drop optional columns rather than wrap into soup.
func TestNarrowTerminalDropsOptionalColumns(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	m.cfg.Render.ShowLimits = true
	m.cfg.Render.ShowRequests = true

	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})

	if m.cfg.Render.ShowBars || m.cfg.Render.ShowLimits || m.cfg.Render.ShowRequests {
		t.Error("a 70-column terminal should drop bars and the request/limit columns")
	}
}

func TestHelpOverlayToggles(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !strings.Contains(m.View(), "Navigate") {
		t.Errorf("help overlay should list key groups:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if strings.Contains(m.View(), "Navigate") {
		t.Error("pressing ? again should close the help overlay")
	}
}

func TestEmptyResultExplainsWhy(t *testing.T) {
	stub := &stubCollector{snap: &inventory.Snapshot{GroupBy: inventory.GroupWorkload, MissingMetrics: 3}}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	if !strings.Contains(m.View(), "no sample") {
		t.Errorf("an empty view should explain the missing metrics:\n%s", m.View())
	}
}

func TestIntervalAdjustmentSteps(t *testing.T) {
	if got := adjustInterval(5*time.Second, 1); got != 10*time.Second {
		t.Errorf("slower from 5s = %v, want 10s", got)
	}
	if got := adjustInterval(5*time.Second, -1); got != 2*time.Second {
		t.Errorf("faster from 5s = %v, want 2s", got)
	}
	if got := adjustInterval(time.Second, -1); got != time.Second {
		t.Errorf("faster from the floor should stay put, got %v", got)
	}
	if got := adjustInterval(5*time.Minute, 1); got != 5*time.Minute {
		t.Errorf("slower from the ceiling should stay put, got %v", got)
	}
}

// Truncation must not slice through an ANSI escape sequence, or the color
// bleeds into the rest of the screen.
func TestTruncatePreservesEscapeSequences(t *testing.T) {
	styled := "\x1b[31mred text that is quite long\x1b[0m"
	got := truncate(styled, 10)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("truncated output should end with a reset: %q", got)
	}
	if strings.Contains(got, "\x1b[3") && !strings.Contains(got, "\x1b[31m") {
		t.Errorf("escape sequence was cut in half: %q", got)
	}
}

// Someone who typed a bare `krm` may not realize they have entered a live,
// full-screen view at all. The footer says so on arrival, then gets out of the
// way and goes back to being a key list.
func TestFooterOrientsOnArrivalThenRevertsToKeys(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	fresh := m.View()
	for _, want := range []string{"live view", "refreshing every", "krm top", "quit"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("opening footer missing %q:\n%s", want, fresh)
		}
	}

	// Wind the clock past the orientation window.
	m.started = time.Now().Add(-2 * orientingWindow)
	settled := m.View()
	if strings.Contains(settled, "krm top") {
		t.Error("the orientation hint should not persist")
	}
	if !strings.Contains(settled, "? help") {
		t.Errorf("footer should revert to the key list:\n%s", settled)
	}
}

func TestHelpOverlayExplainsTheModes(t *testing.T) {
	stub := &stubCollector{snap: testSnapshot()}
	m := newTestModel(t, stub)
	deliver(m, m.collect())

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	out := m.View()
	for _, want := range []string{"Modes", "krm top", "krm watch", "krm notify", "piped"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing %q:\n%s", want, out)
		}
	}
}
