package model

import (
	"testing"
	"time"
)

func usage(cpuUsed, memUsed, cpuLim, memLim int64) Usage {
	u := Usage{
		Used:      Amounts{CPUMilli: cpuUsed, MemBytes: memUsed},
		UsedKnown: true,
	}
	if cpuLim > 0 {
		u.Limits.CPUMilli = cpuLim
		u.HasCPULimit = true
	}
	if memLim > 0 {
		u.Limits.MemBytes = memLim
		u.HasMemLimit = true
	}
	return u
}

func TestRollupSumsChildren(t *testing.T) {
	parent := &Row{
		Kind: KindDeployment, Name: "api",
		Children: []*Row{
			{Kind: KindPod, Name: "api-1", Usage: usage(100, 200<<20, 500, 512<<20)},
			{Kind: KindPod, Name: "api-2", Usage: usage(250, 300<<20, 500, 512<<20)},
		},
	}
	parent.Rollup()

	if got, want := parent.Usage.Used.CPUMilli, int64(350); got != want {
		t.Errorf("cpu sum = %d, want %d", got, want)
	}
	if got, want := parent.Usage.Used.MemBytes, int64(500<<20); got != want {
		t.Errorf("mem sum = %d, want %d", got, want)
	}
	if got, want := parent.Usage.Limits.CPUMilli, int64(1000); got != want {
		t.Errorf("cpu limit sum = %d, want %d", got, want)
	}
	if !parent.Usage.HasCPULimit || !parent.Usage.HasMemLimit {
		t.Error("aggregated limits should be marked present")
	}
	if parent.MetricsMissing {
		t.Error("parent with measured children should not be marked missing")
	}
	if got := parent.PodCount(); got != 2 {
		t.Errorf("PodCount = %d, want 2", got)
	}
}

func TestRollupIsRecursive(t *testing.T) {
	root := &Row{Kind: KindDeployment, Name: "web", Children: []*Row{
		{Kind: KindPod, Name: "web-1", Children: []*Row{
			{Kind: KindContainer, Name: "app", Usage: usage(100, 10, 200, 20)},
			{Kind: KindContainer, Name: "sidecar", Usage: usage(50, 5, 100, 10)},
		}},
	}}
	root.Rollup()

	if got, want := root.Children[0].Usage.Used.CPUMilli, int64(150); got != want {
		t.Errorf("pod cpu = %d, want %d", got, want)
	}
	if got, want := root.Usage.Used.CPUMilli, int64(150); got != want {
		t.Errorf("deployment cpu = %d, want %d", got, want)
	}
	if got, want := root.Usage.Limits.CPUMilli, int64(300); got != want {
		t.Errorf("deployment cpu limit = %d, want %d", got, want)
	}
}

// A container that declares no limit must not drag the aggregate limit to
// zero, but it also must not invent one. Summing the declared limits and
// flagging the column as present is the compromise.
func TestRollupWithPartialLimits(t *testing.T) {
	pod := &Row{Kind: KindPod, Name: "mixed", Children: []*Row{
		{Kind: KindContainer, Name: "app", Usage: usage(100, 0, 400, 0)},
		{Kind: KindContainer, Name: "nolimit", Usage: usage(300, 0, 0, 0)},
	}}
	pod.Rollup()

	if !pod.Usage.HasCPULimit {
		t.Fatal("expected the aggregate to report a CPU limit")
	}
	if got, want := pod.Usage.Limits.CPUMilli, int64(400); got != want {
		t.Errorf("limit = %d, want %d", got, want)
	}
	f, ok := pod.Usage.CPUOfLimit()
	if !ok || f != 1.0 {
		t.Errorf("CPUOfLimit = %v (%v), want 1.0", f, ok)
	}
}

func TestRollupPropagatesMissingMetrics(t *testing.T) {
	dep := &Row{Kind: KindDeployment, Name: "cold", Children: []*Row{
		{Kind: KindPod, Name: "cold-1", MetricsMissing: true},
		{Kind: KindPod, Name: "cold-2", MetricsMissing: true},
	}}
	dep.Rollup()
	if !dep.MetricsMissing {
		t.Error("all children missing metrics should mark the parent missing")
	}

	mixed := &Row{Kind: KindDeployment, Name: "warming", Children: []*Row{
		{Kind: KindPod, Name: "a", MetricsMissing: true},
		{Kind: KindPod, Name: "b", Usage: usage(10, 10, 0, 0)},
	}}
	mixed.Rollup()
	if mixed.MetricsMissing {
		t.Error("a parent with at least one measured child has usable metrics")
	}
}

// Node and PVC rows know their own capacity; rolling up children must not
// clobber it with a sum of zeros.
func TestRollupPreservesParentCapacity(t *testing.T) {
	node := &Row{
		Kind: KindNode, Name: "n1",
		Usage: Usage{Capacity: Amounts{CPUMilli: 4000, MemBytes: 8 << 30}},
		Children: []*Row{
			{Kind: KindPod, Name: "p1", Usage: usage(500, 1<<30, 0, 0)},
		},
	}
	node.Rollup()
	if got, want := node.Usage.Capacity.CPUMilli, int64(4000); got != want {
		t.Errorf("capacity clobbered: got %d, want %d", got, want)
	}
	f, ok := node.Usage.CPUOfCapacity()
	if !ok || f != 0.125 {
		t.Errorf("CPUOfCapacity = %v (%v), want 0.125", f, ok)
	}
}

func TestBestFractionPrefersLimitThenRequest(t *testing.T) {
	withLimit := Usage{
		Used:          Amounts{CPUMilli: 300},
		Requests:      Amounts{CPUMilli: 200},
		Limits:        Amounts{CPUMilli: 600},
		HasCPURequest: true, HasCPULimit: true,
	}
	f, basis, ok := withLimit.BestFraction(MetricCPU)
	if !ok || basis != BasisLimit || f != 0.5 {
		t.Errorf("got %v/%v/%v, want 0.5/limit/true", f, basis, ok)
	}

	requestOnly := Usage{
		Used:          Amounts{CPUMilli: 300},
		Requests:      Amounts{CPUMilli: 200},
		HasCPURequest: true,
	}
	f, basis, ok = requestOnly.BestFraction(MetricCPU)
	if !ok || basis != BasisRequest || f != 1.5 {
		t.Errorf("got %v/%v/%v, want 1.5/request/true", f, basis, ok)
	}

	bare := Usage{Used: Amounts{CPUMilli: 300}}
	if _, _, ok := bare.BestFraction(MetricCPU); ok {
		t.Error("a usage with no denominator should report no fraction")
	}
}

func TestSortDescendingByCPU(t *testing.T) {
	rows := []*Row{
		{Name: "low", Usage: usage(10, 0, 0, 0)},
		{Name: "high", Usage: usage(900, 0, 0, 0)},
		{Name: "mid", Usage: usage(400, 0, 0, 0)},
	}
	Sort(rows, SortCPU, true)
	want := []string{"high", "mid", "low"}
	for i, w := range want {
		if rows[i].Name != w {
			t.Fatalf("position %d = %q, want %q", i, rows[i].Name, w)
		}
	}
}

// Equal values must not shuffle between refreshes, or an idle cluster's table
// flickers every tick.
func TestSortIsStableOnTies(t *testing.T) {
	mk := func() []*Row {
		return []*Row{
			{Name: "alpha", Namespace: "ns", Usage: usage(0, 0, 0, 0)},
			{Name: "bravo", Namespace: "ns", Usage: usage(0, 0, 0, 0)},
			{Name: "charlie", Namespace: "ns", Usage: usage(0, 0, 0, 0)},
		}
	}
	a, b := mk(), mk()
	Sort(a, SortCPU, true)
	Sort(b, SortCPU, true)
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Fatalf("unstable ordering at %d: %q vs %q", i, a[i].Name, b[i].Name)
		}
	}
}

func TestSortPercentPlacesUnknownDenominatorsLast(t *testing.T) {
	rows := []*Row{
		{Name: "nolimit", Usage: Usage{Used: Amounts{CPUMilli: 5000}}},
		{Name: "busy", Usage: usage(90, 0, 100, 0)},
		{Name: "idle", Usage: usage(1, 0, 100, 0)},
	}
	Sort(rows, SortCPUPercent, true)
	if rows[0].Name != "busy" {
		t.Errorf("first = %q, want busy", rows[0].Name)
	}
	if rows[len(rows)-1].Name != "nolimit" {
		t.Errorf("last = %q, want nolimit", rows[len(rows)-1].Name)
	}
}

func TestFlattenRespectsExpansion(t *testing.T) {
	rows := []*Row{{
		Kind: KindDeployment, Name: "api", Namespace: "prod",
		Children: []*Row{{Kind: KindPod, Name: "api-1", Namespace: "prod"}},
	}}

	collapsed := Flatten(rows, func(string) bool { return false })
	if len(collapsed) != 1 {
		t.Fatalf("collapsed length = %d, want 1", len(collapsed))
	}

	expanded := Flatten(rows, func(string) bool { return true })
	if len(expanded) != 2 {
		t.Fatalf("expanded length = %d, want 2", len(expanded))
	}
	if expanded[1].Depth != 1 {
		t.Errorf("child depth = %d, want 1", expanded[1].Depth)
	}
	if expanded[0].Key == expanded[1].Key {
		t.Error("keys must be distinct so expansion state is per-row")
	}
}

func TestFormatCPU(t *testing.T) {
	cases := map[int64]string{0: "0m", 5: "5m", 999: "999m", 1000: "1.00", 2450: "2.45", 12000: "12.0"}
	for in, want := range cases {
		if got := FormatCPU(in); got != want {
			t.Errorf("FormatCPU(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		512:        "512B",
		2048:       "2.0Ki",
		200 << 20:  "200Mi",
		1536 << 20: "1.5Gi",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30s",
		5 * time.Minute:  "5m",
		90 * time.Minute: "1h30m",
		26 * time.Hour:   "1d2h",
	}
	for in, want := range cases {
		if got := FormatAge(in); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatPercentHandlesAbsentDenominator(t *testing.T) {
	if got := FormatPercent(0, false); got != "-" {
		t.Errorf("got %q, want %q", got, "-")
	}
	if got := FormatPercent(0.876, true); got != "88%" {
		t.Errorf("got %q, want %q", got, "88%")
	}
}

// Nodes and volumes measure against capacity, not against the sum of the
// limits their children happen to declare. On an overcommitted node that sum
// routinely exceeds allocatable and bounds nothing.
func TestPreferCapacityChangesTheDenominator(t *testing.T) {
	u := Usage{
		Used:           Amounts{CPUMilli: 4000},
		Limits:         Amounts{CPUMilli: 1000},
		Capacity:       Amounts{CPUMilli: 8000},
		HasCPULimit:    true,
		PreferCapacity: true,
		UsedKnown:      true,
	}
	f, basis, ok := u.BestFraction(MetricCPU)
	if !ok || basis != BasisCapacity || f != 0.5 {
		t.Errorf("got %v/%v/%v, want 0.5/capacity/true", f, basis, ok)
	}

	u.PreferCapacity = false
	f, basis, _ = u.BestFraction(MetricCPU)
	if basis != BasisLimit || f != 4.0 {
		t.Errorf("without the preference the limit wins: got %v/%v", f, basis)
	}
}

// Collect runs Rollup more than once over the same tree, so the preference has
// to survive repeat passes.
func TestRollupPreservesCapacityPreference(t *testing.T) {
	node := &Row{
		Kind: KindNode, Name: "n1",
		Usage: Usage{
			Capacity:       Amounts{CPUMilli: 8000},
			PreferCapacity: true,
		},
		Children: []*Row{
			{Kind: KindPod, Name: "p1", Usage: usage(2000, 0, 500, 0)},
		},
	}
	node.Rollup()
	node.Rollup()

	if !node.Usage.PreferCapacity {
		t.Fatal("capacity preference lost during rollup")
	}
	f, basis, ok := node.Usage.BestFraction(MetricCPU)
	if !ok || basis != BasisCapacity || f != 0.25 {
		t.Errorf("got %v/%v/%v, want 0.25/capacity/true", f, basis, ok)
	}
}
