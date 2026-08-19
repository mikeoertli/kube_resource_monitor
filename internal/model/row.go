package model

import (
	"sort"
	"strings"
	"time"
)

// Kind names the sort of thing a Row describes.
type Kind string

const (
	KindContainer   Kind = "Container"
	KindPod         Kind = "Pod"
	KindDeployment  Kind = "Deployment"
	KindStatefulSet Kind = "StatefulSet"
	KindDaemonSet   Kind = "DaemonSet"
	KindReplicaSet  Kind = "ReplicaSet"
	KindJob         Kind = "Job"
	KindCronJob     Kind = "CronJob"
	KindNode        Kind = "Node"
	KindNamespace   Kind = "Namespace"
	KindPVC         Kind = "PersistentVolumeClaim"
	// KindStandalone covers pods with no controlling owner, so they still get a
	// home when the view groups by workload.
	KindStandalone Kind = "Standalone"
)

// Short returns the abbreviation kubectl users already have in their fingers.
func (k Kind) Short() string {
	switch k {
	case KindContainer:
		return "ctr"
	case KindPod:
		return "pod"
	case KindDeployment:
		return "deploy"
	case KindStatefulSet:
		return "sts"
	case KindDaemonSet:
		return "ds"
	case KindReplicaSet:
		return "rs"
	case KindJob:
		return "job"
	case KindCronJob:
		return "cronjob"
	case KindNode:
		return "node"
	case KindNamespace:
		return "ns"
	case KindPVC:
		return "pvc"
	case KindStandalone:
		return "pod"
	}
	return strings.ToLower(string(k))
}

// Row is one line in the table, and possibly the root of a subtree.
//
// The tree shape is what makes the "roll up or break down" requirement fall
// out naturally: a Deployment row carries its Pod rows as children, and each
// Pod row carries its Containers. Views decide how deep to render; aggregation
// decides what a parent's numbers mean.
type Row struct {
	Kind      Kind              `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Node      string            `json:"node,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`

	Usage Usage `json:"usage"`

	// Ready is the "2/3" style readiness string for workloads and pods.
	Ready string `json:"ready,omitempty"`
	// Phase is the pod phase or an equivalent one-word status.
	Phase    string        `json:"phase,omitempty"`
	Restarts int32         `json:"restarts,omitempty"`
	Age      time.Duration `json:"-"`

	// MetricsMissing marks a row the metrics API had nothing for. Freshly
	// scheduled pods routinely land here for a scrape interval or two, and
	// showing "-" is more honest than showing 0.
	MetricsMissing bool `json:"metricsMissing,omitempty"`

	// Authoritative marks a row whose Usage was computed directly and must not
	// be overwritten by summing children.
	//
	// Pods need this: a pod's effective request is not the sum of its
	// containers' requests, because init containers and sidecars follow the
	// max/sum rules in the pod resource spec. The container children are still
	// worth showing individually, they just are not the source of truth for
	// the pod's own numbers.
	Authoritative bool `json:"-"`

	Children []*Row `json:"children,omitempty"`
}

// PodCount returns how many distinct pods this row covers, which is what the
// replica column shows for workload rows.
func (r *Row) PodCount() int {
	if r.Kind == KindPod || r.Kind == KindStandalone {
		return 1
	}
	n := 0
	for _, c := range r.Children {
		n += c.PodCount()
	}
	return n
}

// Rollup recomputes this row's Usage from its children, bottom-up.
//
// Leaves keep whatever Usage was measured for them directly. Anything with
// children becomes the sum of those children, which is the correct semantics
// for CPU, memory, requests, and limits alike: a Deployment's memory limit is
// the sum of its pods' limits, and that is exactly the number a cluster
// scheduler cares about.
//
// Rows whose children are all missing metrics inherit MetricsMissing, so a
// workload that has scaled to zero or lost its metrics does not masquerade as
// idle-but-healthy.
func (r *Row) Rollup() {
	if len(r.Children) == 0 {
		return
	}
	var sum Usage
	missing := true
	for _, c := range r.Children {
		c.Rollup()
		sum = sum.Add(c.Usage)
		if !c.MetricsMissing {
			missing = false
		}
	}
	if r.Authoritative {
		return
	}
	// Preserve a capacity that was set on the parent directly (nodes and PVCs
	// know their own size; summing children would double-count or zero it),
	// along with the decision to measure against it. Rollup can run more than
	// once over the same tree, so this has to survive repeat passes.
	if !r.Usage.Capacity.IsZero() {
		sum.Capacity = r.Usage.Capacity
	}
	sum.PreferCapacity = sum.PreferCapacity || r.Usage.PreferCapacity
	r.Usage = sum
	r.MetricsMissing = missing
	r.Usage.UsedKnown = !missing
}

// SortKey names an ordering for rows.
type SortKey string

const (
	SortName       SortKey = "name"
	SortCPU        SortKey = "cpu"
	SortMemory     SortKey = "memory"
	SortStorage    SortKey = "storage"
	SortCPUPercent SortKey = "cpu%"
	SortMemPercent SortKey = "mem%"
	SortRestarts   SortKey = "restarts"
)

// AllSortKeys is the cycle order used by the TUI's sort toggle.
var AllSortKeys = []SortKey{SortCPU, SortMemory, SortCPUPercent, SortMemPercent, SortName, SortRestarts, SortStorage}

// Sort orders rows in place, recursing into children so an expanded subtree
// follows the same ordering as its parent list.
//
// Ties break on namespace then name, which keeps the display stable between
// refreshes; without that, two idle pods at 0m would swap places every tick and
// make the table visually noisy.
func Sort(rows []*Row, key SortKey, descending bool) {
	less := func(a, b *Row) bool {
		switch key {
		case SortCPU:
			if a.Usage.Used.CPUMilli != b.Usage.Used.CPUMilli {
				return a.Usage.Used.CPUMilli < b.Usage.Used.CPUMilli
			}
		case SortMemory:
			if a.Usage.Used.MemBytes != b.Usage.Used.MemBytes {
				return a.Usage.Used.MemBytes < b.Usage.Used.MemBytes
			}
		case SortStorage:
			if a.Usage.Used.StorageBytes != b.Usage.Used.StorageBytes {
				return a.Usage.Used.StorageBytes < b.Usage.Used.StorageBytes
			}
		case SortCPUPercent:
			af, aok, _ := fractionOrZero(a, MetricCPU)
			bf, bok, _ := fractionOrZero(b, MetricCPU)
			if aok != bok {
				return !aok // rows without a denominator sort last when descending
			}
			if af != bf {
				return af < bf
			}
		case SortMemPercent:
			af, aok, _ := fractionOrZero(a, MetricMemory)
			bf, bok, _ := fractionOrZero(b, MetricMemory)
			if aok != bok {
				return !aok
			}
			if af != bf {
				return af < bf
			}
		case SortRestarts:
			if a.Restarts != b.Restarts {
				return a.Restarts < b.Restarts
			}
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if key == SortName {
			if rows[i].Namespace != rows[j].Namespace {
				lt := rows[i].Namespace < rows[j].Namespace
				if descending {
					return !lt
				}
				return lt
			}
			lt := rows[i].Name < rows[j].Name
			if descending {
				return !lt
			}
			return lt
		}
		if descending {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})

	for _, r := range rows {
		Sort(r.Children, key, descending)
	}
}

func fractionOrZero(r *Row, m Metric) (float64, bool, Basis) {
	f, b, ok := r.Usage.BestFraction(m)
	return f, ok, b
}

// Flatten walks the tree depth-first and returns every row at or below the
// given depth, paired with its indent level.
//
// depth < 0 means unlimited. The expanded set lets the TUI keep per-row
// expansion state without mutating the tree it re-fetches every tick.
type FlatRow struct {
	Row   *Row
	Depth int
	// Key is the stable identity used to remember expansion across refreshes.
	Key string
}

// Flatten produces the display list. expanded reports whether a given row key
// should have its children included; pass nil to expand nothing.
func Flatten(rows []*Row, expanded func(key string) bool) []FlatRow {
	var out []FlatRow
	var walk func(rs []*Row, depth int, prefix string)
	walk = func(rs []*Row, depth int, prefix string) {
		for _, r := range rs {
			key := prefix + "/" + string(r.Kind) + ":" + r.Namespace + "/" + r.Name
			out = append(out, FlatRow{Row: r, Depth: depth, Key: key})
			if len(r.Children) > 0 && expanded != nil && expanded(key) {
				walk(r.Children, depth+1, key)
			}
		}
	}
	walk(rows, 0, "")
	return out
}

// TotalOf sums the usage of a slice of rows, for the footer line.
func TotalOf(rows []*Row) Usage {
	var sum Usage
	for _, r := range rows {
		sum = sum.Add(r.Usage)
	}
	return sum
}
