// Package inventory joins what the cluster declares (pods, workloads, nodes,
// volumes and their requests and limits) with what it is actually consuming,
// and shapes the result into the row tree the views render.
package inventory

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/mikeoertli/kube_resource_monitor/internal/metrics"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// GroupBy selects the top level of the row tree.
type GroupBy string

const (
	// GroupWorkload resolves each pod to its controlling Deployment/StatefulSet/
	// DaemonSet/Job and groups there. This is the default because it matches how
	// people reason about their own systems.
	GroupWorkload    GroupBy = "workload"
	GroupPod         GroupBy = "pod"
	GroupContainer   GroupBy = "container"
	GroupNode        GroupBy = "node"
	GroupNamespace   GroupBy = "namespace"
	GroupPVC         GroupBy = "pvc"
	GroupDeployment  GroupBy = "deployment"
	GroupStatefulSet GroupBy = "statefulset"
	GroupDaemonSet   GroupBy = "daemonset"
	GroupReplicaSet  GroupBy = "replicaset"
	GroupJob         GroupBy = "job"
	GroupCronJob     GroupBy = "cronjob"
)

// AllGroupBy is the cycle order used by the TUI's group toggle.
var AllGroupBy = []GroupBy{GroupWorkload, GroupPod, GroupContainer, GroupNode, GroupNamespace, GroupPVC}

// ParseGroupBy validates a --group-by value.
func ParseGroupBy(s string) (GroupBy, error) {
	switch g := GroupBy(strings.ToLower(strings.TrimSpace(s))); g {
	case GroupWorkload, GroupPod, GroupContainer, GroupNode, GroupNamespace, GroupPVC,
		GroupDeployment, GroupStatefulSet, GroupDaemonSet, GroupReplicaSet, GroupJob, GroupCronJob:
		return g, nil
	case "":
		return GroupWorkload, nil
	// Accept the abbreviations people already type at kubectl.
	case "deploy", "deployments":
		return GroupDeployment, nil
	case "sts", "statefulsets":
		return GroupStatefulSet, nil
	case "ds", "daemonsets":
		return GroupDaemonSet, nil
	case "rs", "replicasets":
		return GroupReplicaSet, nil
	case "po", "pods":
		return GroupPod, nil
	case "ctr", "containers":
		return GroupContainer, nil
	case "no", "nodes":
		return GroupNode, nil
	case "ns", "namespaces":
		return GroupNamespace, nil
	case "pvcs", "volume", "volumes":
		return GroupPVC, nil
	default:
		return "", fmt.Errorf("unknown group-by %q (try: workload, pod, container, node, namespace, pvc, deployment, statefulset, daemonset, job)", s)
	}
}

// kindFilter returns the workload kind a GroupBy restricts to, if any.
func (g GroupBy) kindFilter() (model.Kind, bool) {
	switch g {
	case GroupDeployment:
		return model.KindDeployment, true
	case GroupStatefulSet:
		return model.KindStatefulSet, true
	case GroupDaemonSet:
		return model.KindDaemonSet, true
	case GroupReplicaSet:
		return model.KindReplicaSet, true
	case GroupJob:
		return model.KindJob, true
	case GroupCronJob:
		return model.KindCronJob, true
	}
	return "", false
}

// Options controls one collection pass.
type Options struct {
	// Namespace scopes the query; empty means all namespaces.
	Namespace string
	// LabelSelector is a standard Kubernetes selector ("app=web,tier!=cache").
	LabelSelector string
	// NamePattern filters rows by name. Treated as a regular expression when
	// it compiles as one, otherwise as a case-insensitive substring.
	NamePattern string
	// FieldSelector is passed through to the pod list ("spec.nodeName=n1").
	FieldSelector string

	GroupBy GroupBy
	// IncludeContainers attaches per-container children to pod rows.
	IncludeContainers bool
	// IncludeMissing keeps pods the metrics API had no sample for.
	IncludeMissing bool
	// OnlyProblems keeps rows exceeding ProblemThreshold on any metric.
	OnlyProblems     bool
	ProblemThreshold float64
}

// Snapshot is one complete collection pass.
type Snapshot struct {
	Rows    []*model.Row
	Totals  model.Usage
	Taken   time.Time
	GroupBy GroupBy
	// Window is the averaging interval reported by the metrics source.
	Window time.Duration
	// Warnings holds non-fatal degradations: a node whose kubelet was
	// unreachable, an RBAC gap on Jobs, and so on. The UI shows these rather
	// than pretending the picture is complete.
	Warnings []string
	// PodCount and MissingMetrics summarize coverage.
	PodCount       int
	MissingMetrics int
}

// Collector performs collection passes against one cluster.
type Collector struct {
	kube     kubernetes.Interface
	provider metrics.Provider
}

// New builds a collector.
func New(k kubernetes.Interface, p metrics.Provider) *Collector {
	return &Collector{kube: k, provider: p}
}

// Collect gathers one snapshot.
func (c *Collector) Collect(ctx context.Context, opts Options) (*Snapshot, error) {
	if opts.GroupBy == "" {
		opts.GroupBy = GroupWorkload
	}
	snap := &Snapshot{Taken: time.Now(), GroupBy: opts.GroupBy}

	if opts.GroupBy == GroupPVC {
		return c.collectPVC(ctx, opts, snap)
	}

	pods, err := c.kube.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
		FieldSelector: opts.FieldSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	samples, err := c.provider.PodMetrics(ctx, opts.Namespace, opts.LabelSelector)
	if err != nil {
		return nil, err
	}
	idx := metrics.IndexPods(samples)
	if len(samples) > 0 {
		snap.Window = samples[0].Window
	}

	podRows := make([]*model.Row, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Terminal pods have no ongoing consumption and only add noise; their
		// requests no longer occupy the scheduler either.
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		row := c.podRow(pod, idx, opts.IncludeContainers)
		if row.MetricsMissing {
			snap.MissingMetrics++
			if !opts.IncludeMissing && opts.GroupBy != GroupWorkload {
				continue
			}
		}
		podRows = append(podRows, row)
	}
	snap.PodCount = len(podRows)

	switch opts.GroupBy {
	case GroupPod:
		snap.Rows = podRows
	case GroupContainer:
		snap.Rows = flattenToContainers(podRows)
	case GroupNamespace:
		snap.Rows = groupByNamespace(podRows)
	case GroupNode:
		rows, warns := c.groupByNode(ctx, podRows)
		snap.Rows = rows
		snap.Warnings = append(snap.Warnings, warns...)
	default:
		rows, warns := c.groupByWorkload(ctx, opts, podRows)
		snap.Rows = rows
		snap.Warnings = append(snap.Warnings, warns...)
	}

	for _, r := range snap.Rows {
		r.Rollup()
	}
	snap.Rows = filterByName(snap.Rows, opts.NamePattern)
	if opts.OnlyProblems {
		snap.Rows = filterProblems(snap.Rows, opts.ProblemThreshold)
	}
	snap.Totals = model.TotalOf(snap.Rows)
	return snap, nil
}

// podRow builds a single pod's row, joining spec with measurement.
func (c *Collector) podRow(pod *corev1.Pod, idx metrics.PodSampleIndex, includeContainers bool) *model.Row {
	row := &model.Row{
		Kind:          model.KindPod,
		Name:          pod.Name,
		Namespace:     pod.Namespace,
		Node:          pod.Spec.NodeName,
		Labels:        pod.Labels,
		Phase:         string(pod.Status.Phase),
		Authoritative: true,
	}
	if !pod.CreationTimestamp.IsZero() {
		row.Age = time.Since(pod.CreationTimestamp.Time)
	}

	ready, total := 0, len(pod.Status.ContainerStatuses)
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Ready {
			ready++
		}
		row.Restarts += cs.RestartCount
	}
	if total > 0 {
		row.Ready = fmt.Sprintf("%d/%d", ready, total)
	}

	applyDeclared(&row.Usage, effectivePodResources(&pod.Spec))

	sample, haveSample := idx.Get(pod.Namespace, pod.Name)
	row.MetricsMissing = !haveSample
	row.Usage.UsedKnown = haveSample
	if haveSample {
		row.Usage.Used = sample.Total()
	}

	if includeContainers {
		row.Children = c.containerRows(pod, sample, haveSample)
	}
	return row
}

func (c *Collector) containerRows(pod *corev1.Pod, sample metrics.PodSample, haveSample bool) []*model.Row {
	specs := make([]*corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for i := range pod.Spec.Containers {
		specs = append(specs, &pod.Spec.Containers[i])
	}
	// Sidecars run alongside the app for the pod's whole life, so they belong
	// in the container breakdown. One-shot init containers do not: they have
	// already exited and would show a permanent 0m.
	for i := range pod.Spec.InitContainers {
		if isSidecar(&pod.Spec.InitContainers[i]) {
			specs = append(specs, &pod.Spec.InitContainers[i])
		}
	}

	restarts := map[string]int32{}
	ready := map[string]bool{}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		restarts[cs.Name] = cs.RestartCount
		ready[cs.Name] = cs.Ready
	}

	out := make([]*model.Row, 0, len(specs))
	for _, spec := range specs {
		cr := &model.Row{
			Kind:      model.KindContainer,
			Name:      spec.Name,
			Namespace: pod.Namespace,
			Node:      pod.Spec.NodeName,
			Restarts:  restarts[spec.Name],
		}
		applyDeclared(&cr.Usage, containerResources(spec))
		if haveSample {
			if used, ok := sample.Containers[spec.Name]; ok {
				cr.Usage.Used = used
				cr.Usage.UsedKnown = true
			} else {
				cr.MetricsMissing = true
			}
		} else {
			cr.MetricsMissing = true
		}
		if r, ok := ready[spec.Name]; ok && !r {
			cr.Phase = "NotReady"
		}
		out = append(out, cr)
	}
	return out
}

func flattenToContainers(podRows []*model.Row) []*model.Row {
	var out []*model.Row
	for _, p := range podRows {
		if len(p.Children) == 0 {
			out = append(out, p)
			continue
		}
		for _, c := range p.Children {
			// Qualify the container name with its pod, or every "nginx" in the
			// namespace becomes indistinguishable.
			clone := *c
			clone.Name = p.Name + "/" + c.Name
			clone.Labels = p.Labels
			out = append(out, &clone)
		}
	}
	return out
}

func groupByNamespace(podRows []*model.Row) []*model.Row {
	byNS := map[string]*model.Row{}
	var order []string
	for _, p := range podRows {
		g, ok := byNS[p.Namespace]
		if !ok {
			g = &model.Row{Kind: model.KindNamespace, Name: p.Namespace, Namespace: p.Namespace}
			byNS[p.Namespace] = g
			order = append(order, p.Namespace)
		}
		g.Children = append(g.Children, p)
	}
	sort.Strings(order)
	out := make([]*model.Row, 0, len(order))
	for _, ns := range order {
		out = append(out, byNS[ns])
	}
	return out
}

// groupByNode buckets pods under the node running them and attaches each
// node's allocatable as capacity, which turns the percentage column into
// "how full is this machine".
func (c *Collector) groupByNode(ctx context.Context, podRows []*model.Row) ([]*model.Row, []string) {
	var warnings []string

	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	allocatable := map[string]model.Amounts{}
	if err != nil {
		warnings = append(warnings, "could not list nodes: "+err.Error()+" (node capacity unavailable)")
	} else {
		for i := range nodes.Items {
			n := &nodes.Items[i]
			var a model.Amounts
			if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
				a.CPUMilli = q.MilliValue()
			}
			if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
				a.MemBytes = q.Value()
			}
			allocatable[n.Name] = a
		}
	}

	// Prefer node-level metrics where available: a node reports its own total
	// consumption including kubelet, container runtime, and system daemons,
	// which is strictly larger than the sum of its pods and is the number that
	// determines whether the machine is actually in trouble.
	nodeUsage := map[string]model.Amounts{}
	if samples, err := c.provider.NodeMetrics(ctx); err != nil {
		warnings = append(warnings, "node metrics unavailable: "+err.Error()+" (falling back to the sum of pods)")
	} else {
		for _, s := range samples {
			nodeUsage[s.Name] = s.Usage
		}
	}

	byNode := map[string]*model.Row{}
	var order []string
	for _, p := range podRows {
		name := p.Node
		if name == "" {
			name = "(unscheduled)"
		}
		g, ok := byNode[name]
		if !ok {
			g = &model.Row{Kind: model.KindNode, Name: name}
			if a, ok := allocatable[name]; ok {
				g.Usage.Capacity = a
			}
			byNode[name] = g
			order = append(order, name)
		}
		g.Children = append(g.Children, p)
	}
	// Include idle nodes: a node with no pods is exactly the thing you want to
	// notice when you are hunting for capacity.
	for name, a := range allocatable {
		if _, ok := byNode[name]; !ok {
			byNode[name] = &model.Row{Kind: model.KindNode, Name: name, Usage: model.Usage{Capacity: a}}
			order = append(order, name)
		}
	}
	sort.Strings(order)

	out := make([]*model.Row, 0, len(order))
	for _, name := range order {
		row := byNode[name]
		capacity := row.Usage.Capacity
		row.Rollup()
		// Rollup replaced the node's Usage with the sum of its pods, which
		// carries their limits. Restore the machine's own capacity and mark it
		// as the denominator: summed pod limits routinely exceed allocatable on
		// an overcommitted node and bound nothing.
		row.Usage.Capacity = capacity
		row.Usage.PreferCapacity = true
		if u, ok := nodeUsage[name]; ok {
			row.Usage.Used = u
			row.Usage.UsedKnown = true
			row.MetricsMissing = false
			row.Authoritative = true
		}
		out = append(out, row)
	}
	return out, warnings
}

func (c *Collector) groupByWorkload(ctx context.Context, opts Options, podRows []*model.Row) ([]*model.Row, []string) {
	var warnings []string

	resolver := newOwnerResolver(c.kube)
	if err := resolver.prime(ctx, opts.Namespace); err != nil {
		warnings = append(warnings, "owner resolution degraded: "+err.Error()+" (pods will group under their direct controller)")
	}

	// Re-fetch the pod objects we need ownerReferences from. Rows do not carry
	// the full object, and re-listing is cheaper than threading it through.
	pods, err := c.kube.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
		FieldSelector: opts.FieldSelector,
	})
	if err != nil {
		warnings = append(warnings, "could not re-list pods for grouping: "+err.Error())
		return podRows, warnings
	}
	ownerOf := map[string]Owner{}
	for i := range pods.Items {
		p := &pods.Items[i]
		ownerOf[p.Namespace+"/"+p.Name] = resolver.resolve(p)
	}

	wantKind, restrict := opts.GroupBy.kindFilter()

	groups := map[string]*model.Row{}
	var order []string
	var standalone []*model.Row
	for _, p := range podRows {
		o, ok := ownerOf[p.Namespace+"/"+p.Name]
		if !ok {
			o = Owner{Kind: model.KindStandalone, Name: p.Name, Namespace: p.Namespace}
		}
		if restrict && o.Kind != wantKind {
			continue
		}
		// A pod with no controller is its own group of one. Wrapping it in a
		// parent row would render the same name twice, once as a group header
		// and once as its only child.
		if o.Kind == model.KindStandalone {
			if !p.MetricsMissing || opts.IncludeMissing {
				standalone = append(standalone, p)
			}
			continue
		}
		key := o.Key()
		g, exists := groups[key]
		if !exists {
			g = &model.Row{Kind: o.Kind, Name: o.Name, Namespace: o.Namespace, Labels: p.Labels}
			groups[key] = g
			order = append(order, key)
		}
		g.Children = append(g.Children, p)
	}

	// Declared replica counts let us show "2/3" on a workload whose third pod
	// has not been created yet, which pod-derived counting cannot see.
	if restrict || opts.GroupBy == GroupWorkload {
		c.annotateReplicas(ctx, opts.Namespace, groups)
	}

	sort.Strings(order)
	out := make([]*model.Row, 0, len(order)+len(standalone))
	for _, k := range order {
		g := groups[k]
		// Pods with no metrics are kept inside a workload so its replica count
		// and declared totals stay honest, but a workload where *every* pod is
		// unscraped has nothing to show and would contradict the "n pods
		// omitted" footer.
		g.Rollup()
		if g.MetricsMissing && !opts.IncludeMissing {
			continue
		}
		out = append(out, g)
	}
	out = append(out, standalone...)
	return out, warnings
}

// annotateReplicas fills in ready/desired for workload rows.
func (c *Collector) annotateReplicas(ctx context.Context, namespace string, groups map[string]*model.Row) {
	setReady := func(kind model.Kind, ns, name string, ready, desired int32) {
		if g, ok := groups[Owner{Kind: kind, Name: name, Namespace: ns}.Key()]; ok {
			g.Ready = fmt.Sprintf("%d/%d", ready, desired)
		}
	}

	if deps, err := c.kube.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range deps.Items {
			d := &deps.Items[i]
			setReady(model.KindDeployment, d.Namespace, d.Name, d.Status.ReadyReplicas, desiredReplicas(d.Spec.Replicas))
		}
	}
	if sts, err := c.kube.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range sts.Items {
			s := &sts.Items[i]
			setReady(model.KindStatefulSet, s.Namespace, s.Name, s.Status.ReadyReplicas, desiredReplicas(s.Spec.Replicas))
		}
	}
	if ds, err := c.kube.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range ds.Items {
			d := &ds.Items[i]
			setReady(model.KindDaemonSet, d.Namespace, d.Name, d.Status.NumberReady, d.Status.DesiredNumberScheduled)
		}
	}
	if rs, err := c.kube.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range rs.Items {
			r := &rs.Items[i]
			setReady(model.KindReplicaSet, r.Namespace, r.Name, r.Status.ReadyReplicas, desiredReplicas(r.Spec.Replicas))
		}
	}
}

func desiredReplicas(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

// filterByName keeps rows whose own name matches, or that contain a matching
// descendant.
//
// Keeping a parent whose child matches is what makes `--filter web-7d9f` useful
// when grouping by workload: you searched for a pod, so the workload it lives
// in is the right thing to show you.
func filterByName(rows []*model.Row, pattern string) []*model.Row {
	if strings.TrimSpace(pattern) == "" {
		return rows
	}
	match := nameMatcher(pattern)

	var keep func(r *model.Row) bool
	keep = func(r *model.Row) bool {
		if match(r.Name) {
			return true
		}
		for _, c := range r.Children {
			if keep(c) {
				return true
			}
		}
		return false
	}

	out := make([]*model.Row, 0, len(rows))
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// nameMatcher compiles a filter into a predicate.
//
// Regex when it parses, case-insensitive substring otherwise. Typing "web-"
// should just work; typing "^(api|web)-" should also just work. Falling back
// rather than erroring means a half-typed regex in the TUI's live filter box
// degrades to a substring search instead of blanking the table.
func nameMatcher(pattern string) func(string) bool {
	if re, err := regexp.Compile("(?i)" + pattern); err == nil {
		return re.MatchString
	}
	lower := strings.ToLower(pattern)
	return func(s string) bool { return strings.Contains(strings.ToLower(s), lower) }
}

// filterProblems keeps rows at or above a fraction of any denominator.
func filterProblems(rows []*model.Row, threshold float64) []*model.Row {
	if threshold <= 0 {
		threshold = 0.85
	}
	out := make([]*model.Row, 0, len(rows))
	for _, r := range rows {
		hot := false
		for _, m := range []model.Metric{model.MetricCPU, model.MetricMemory, model.MetricStorage} {
			if f, _, ok := r.Usage.BestFraction(m); ok && f >= threshold {
				hot = true
				break
			}
		}
		if hot || r.Restarts > 0 && r.Kind == model.KindPod {
			out = append(out, r)
		}
	}
	return out
}
