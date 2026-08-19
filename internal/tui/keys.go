package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap is the full binding set. Keeping it in one place lets the help view be
// generated from the same source the handler switches on, so the two cannot
// drift apart.
type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	PageUp      key.Binding
	PageDown    key.Binding
	Home        key.Binding
	End         key.Binding
	Expand      key.Binding
	ExpandAll   key.Binding
	CollapseAll key.Binding
	Sort        key.Binding
	SortReverse key.Binding
	Group       key.Binding
	Filter      key.Binding
	ClearFilter key.Binding
	Containers  key.Binding
	Requests    key.Binding
	Limits      key.Binding
	Bars        key.Binding
	Pause       key.Binding
	Refresh     key.Binding
	Faster      key.Binding
	Slower      key.Binding
	Namespaces  key.Binding
	OnlyProblem key.Binding
	Help        key.Binding
	Quit        key.Binding
}

var keys = keyMap{
	Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	PageUp:      key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
	PageDown:    key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
	Home:        key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "top")),
	End:         key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "bottom")),
	Expand:      key.NewBinding(key.WithKeys("enter", " ", "right"), key.WithHelp("↵", "expand/collapse")),
	ExpandAll:   key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "expand all")),
	CollapseAll: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "collapse all")),
	Sort:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "cycle sort")),
	SortReverse: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse sort")),
	Group:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "cycle grouping")),
	Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	ClearFilter: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
	Containers:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "containers")),
	Requests:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "requests column")),
	Limits:      key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "limits column")),
	Bars:        key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "bars")),
	Pause:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
	Refresh:     key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh now")),
	Faster:      key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "faster")),
	Slower:      key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "slower")),
	Namespaces:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all namespaces")),
	OnlyProblem: key.NewBinding(key.WithKeys("!"), key.WithHelp("!", "only hot rows")),
	Help:        key.NewBinding(key.WithKeys("?", "h"), key.WithHelp("?", "help")),
	Quit:        key.NewBinding(key.WithKeys("ctrl+c", "Q"), key.WithHelp("Q", "quit")),
}

// helpGroups drives the help overlay.
var helpGroups = []struct {
	Title    string
	Bindings []key.Binding
}{
	{"Navigate", []key.Binding{keys.Up, keys.Down, keys.PageUp, keys.PageDown, keys.Home, keys.End}},
	{"Drill down", []key.Binding{keys.Expand, keys.ExpandAll, keys.CollapseAll, keys.Containers}},
	{"Arrange", []key.Binding{keys.Sort, keys.SortReverse, keys.Group, keys.OnlyProblem}},
	{"Filter", []key.Binding{keys.Filter, keys.ClearFilter, keys.Namespaces}},
	{"Columns", []key.Binding{keys.Requests, keys.Limits, keys.Bars}},
	{"Watch", []key.Binding{keys.Pause, keys.Refresh, keys.Faster, keys.Slower}},
	{"Other", []key.Binding{keys.Help, keys.Quit}},
}
