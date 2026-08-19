package inventory

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mikeoertli/kube_resource_monitor/internal/metrics"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

func qty(s string) resource.Quantity { return resource.MustParse(s) }

func container(name, cpuReq, memReq, cpuLim, memLim string) corev1.Container {
	c := corev1.Container{Name: name, Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}}
	if cpuReq != "" {
		c.Resources.Requests[corev1.ResourceCPU] = qty(cpuReq)
	}
	if memReq != "" {
		c.Resources.Requests[corev1.ResourceMemory] = qty(memReq)
	}
	if cpuLim != "" {
		c.Resources.Limits[corev1.ResourceCPU] = qty(cpuLim)
	}
	if memLim != "" {
		c.Resources.Limits[corev1.ResourceMemory] = qty(memLim)
	}
	return c
}

func pod(ns, name, node string, owner *metav1.OwnerReference, labels map[string]string, cs ...corev1.Container) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node, Containers: cs},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, c := range cs {
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses,
			corev1.ContainerStatus{Name: c.Name, Ready: true})
	}
	if owner != nil {
		p.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return p
}

func ctrlRef(kind, name string) *metav1.OwnerReference {
	t := true
	return &metav1.OwnerReference{Kind: kind, Name: name, Controller: &t}
}

// testCluster builds a small but realistic namespace: a Deployment with two
// pods (via a ReplicaSet), a StatefulSet pod, and a standalone pod.
func testCluster() (*fake.Clientset, *metrics.Mock) {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: "prod", Name: "web-abc123",
		OwnerReferences: []metav1.OwnerReference{*ctrlRef("Deployment", "web")},
	}}
	two := int32(2)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: &two},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}

	web1 := pod("prod", "web-abc123-aaaaa", "node-1", ctrlRef("ReplicaSet", "web-abc123"),
		map[string]string{"app": "web"},
		container("nginx", "100m", "128Mi", "500m", "512Mi"),
		container("sidecar", "50m", "64Mi", "100m", "128Mi"))
	web2 := pod("prod", "web-abc123-bbbbb", "node-2", ctrlRef("ReplicaSet", "web-abc123"),
		map[string]string{"app": "web"},
		container("nginx", "100m", "128Mi", "500m", "512Mi"),
		container("sidecar", "50m", "64Mi", "100m", "128Mi"))
	db := pod("prod", "db-0", "node-1", ctrlRef("StatefulSet", "db"),
		map[string]string{"app": "db"},
		container("postgres", "500m", "1Gi", "2", "4Gi"))
	loose := pod("prod", "debug-shell", "node-2", nil, nil,
		container("shell", "", "", "", ""))

	node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: qty("4"), corev1.ResourceMemory: qty("16Gi")},
	}}
	node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: qty("4"), corev1.ResourceMemory: qty("16Gi")},
	}}

	kc := fake.NewSimpleClientset(rs, dep, web1, web2, db, loose, node1, node2)

	mock := metrics.NewMock([]metrics.MockPod{
		{Namespace: "prod", Name: "web-abc123-aaaaa", Baseline: map[string]model.Amounts{
			"nginx":   {CPUMilli: 200, MemBytes: 200 << 20},
			"sidecar": {CPUMilli: 20, MemBytes: 20 << 20},
		}},
		{Namespace: "prod", Name: "web-abc123-bbbbb", Baseline: map[string]model.Amounts{
			"nginx":   {CPUMilli: 300, MemBytes: 250 << 20},
			"sidecar": {CPUMilli: 30, MemBytes: 25 << 20},
		}},
		{Namespace: "prod", Name: "db-0", Baseline: map[string]model.Amounts{
			"postgres": {CPUMilli: 1500, MemBytes: 3 << 30},
		}},
		// debug-shell deliberately has no sample, standing in for a pod the
		// metrics-server has not scraped yet.
	}, []metrics.MockNode{
		{Name: "node-1", Baseline: model.Amounts{CPUMilli: 2000, MemBytes: 5 << 30}},
		{Name: "node-2", Baseline: model.Amounts{CPUMilli: 800, MemBytes: 2 << 30}},
	}, nil)
	mock.Jitter = 0 // deterministic

	return kc, mock
}

func findRow(rows []*model.Row, name string) *model.Row {
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func TestCollectGroupsByWorkload(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupWorkload,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	web := findRow(snap.Rows, "web")
	if web == nil {
		t.Fatalf("no 'web' row; got %v", names(snap.Rows))
	}
	if web.Kind != model.KindDeployment {
		t.Errorf("web kind = %s, want Deployment", web.Kind)
	}
	// The pods must have been resolved through the ReplicaSet to the Deployment.
	if got := web.PodCount(); got != 2 {
		t.Errorf("web pod count = %d, want 2", got)
	}
	// 200+20 + 300+30
	if got, want := web.Usage.Used.CPUMilli, int64(550); got != want {
		t.Errorf("web cpu = %d, want %d", got, want)
	}
	// Two pods x (500m + 100m)
	if got, want := web.Usage.Limits.CPUMilli, int64(1200); got != want {
		t.Errorf("web cpu limit = %d, want %d", got, want)
	}
	if web.Ready != "2/2" {
		t.Errorf("web ready = %q, want 2/2", web.Ready)
	}

	if db := findRow(snap.Rows, "db"); db == nil || db.Kind != model.KindStatefulSet {
		t.Errorf("expected a StatefulSet row named db, got %v", names(snap.Rows))
	}
	// debug-shell has no controller *and* no metrics sample, so it is omitted
	// for the same reason an unscraped pod is omitted from the pod view.
	if findRow(snap.Rows, "debug-shell") != nil {
		t.Errorf("unscraped standalone pod should be omitted; got %v", names(snap.Rows))
	}
}

// A pod with no controller is its own group of one. Wrapping it in a parent
// row would print the same name twice, once as a header and once as its child.
func TestStandalonePodsAreNotDoubleNested(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupWorkload, IncludeMissing: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	loose := findRow(snap.Rows, "debug-shell")
	if loose == nil {
		t.Fatalf("standalone pod dropped; got %v", names(snap.Rows))
	}
	if len(loose.Children) != 0 {
		t.Errorf("standalone pod should be a leaf, got %d children", len(loose.Children))
	}
	if loose.Kind != model.KindPod {
		t.Errorf("kind = %s, want Pod", loose.Kind)
	}
	if !loose.MetricsMissing {
		t.Error("debug-shell has no sample and should be flagged as missing metrics")
	}
}

// Keeping unscraped pods inside a workload keeps its replica count honest, but
// a workload where every pod is unscraped has nothing to show.
func TestWorkloadWithNoScrapedPodsIsOmitted(t *testing.T) {
	kc, _ := testCluster()
	empty := metrics.NewMock(nil, nil, nil)

	snap, err := New(kc, empty).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupWorkload,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Rows) != 0 {
		t.Errorf("expected no rows when nothing has been scraped, got %v", names(snap.Rows))
	}

	withMissing, err := New(kc, empty).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupWorkload, IncludeMissing: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if findRow(withMissing.Rows, "web") == nil {
		t.Errorf("--include-missing should keep the workload: %v", names(withMissing.Rows))
	}
}

func TestCollectGroupsByPod(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupPod,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	// debug-shell has no metrics and IncludeMissing is false.
	if r := findRow(snap.Rows, "debug-shell"); r != nil {
		t.Error("pod without metrics should be omitted unless IncludeMissing is set")
	}
	if got := len(snap.Rows); got != 3 {
		t.Fatalf("row count = %d, want 3: %v", got, names(snap.Rows))
	}
	p := findRow(snap.Rows, "web-abc123-aaaaa")
	if got, want := p.Usage.Used.CPUMilli, int64(220); got != want {
		t.Errorf("pod cpu = %d, want %d", got, want)
	}
	if got, want := p.Usage.Requests.CPUMilli, int64(150); got != want {
		t.Errorf("pod cpu request = %d, want %d", got, want)
	}
}

func TestIncludeMissingKeepsUnscrapedPods(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupPod, IncludeMissing: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	r := findRow(snap.Rows, "debug-shell")
	if r == nil {
		t.Fatal("expected debug-shell to be included")
	}
	if !r.MetricsMissing {
		t.Error("debug-shell should be flagged as missing metrics")
	}
	if snap.MissingMetrics != 1 {
		t.Errorf("MissingMetrics = %d, want 1", snap.MissingMetrics)
	}
}

func TestCollectWithContainers(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupPod, IncludeContainers: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	p := findRow(snap.Rows, "web-abc123-aaaaa")
	if len(p.Children) != 2 {
		t.Fatalf("container count = %d, want 2", len(p.Children))
	}
	nginx := findRow(p.Children, "nginx")
	if nginx == nil {
		t.Fatal("no nginx container row")
	}
	if got, want := nginx.Usage.Used.CPUMilli, int64(200); got != want {
		t.Errorf("nginx cpu = %d, want %d", got, want)
	}
	if got, want := nginx.Usage.Limits.CPUMilli, int64(500); got != want {
		t.Errorf("nginx cpu limit = %d, want %d", got, want)
	}
}

// Container rows must stay attributable to their pod once flattened, or two
// containers named "nginx" become one indistinguishable pair of rows.
func TestGroupByContainerQualifiesNames(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupContainer, IncludeContainers: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if findRow(snap.Rows, "web-abc123-aaaaa/nginx") == nil {
		t.Errorf("expected pod-qualified container name; got %v", names(snap.Rows))
	}
}

func TestGroupByNodeUsesNodeMetricsAndAllocatable(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{GroupBy: GroupNode})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	n1 := findRow(snap.Rows, "node-1")
	if n1 == nil {
		t.Fatalf("no node-1 row; got %v", names(snap.Rows))
	}
	// Node-level metrics include system daemons and must win over the sum of pods.
	if got, want := n1.Usage.Used.CPUMilli, int64(2000); got != want {
		t.Errorf("node cpu = %d, want %d (node metrics should override the pod sum)", got, want)
	}
	if got, want := n1.Usage.Capacity.CPUMilli, int64(4000); got != want {
		t.Errorf("node allocatable cpu = %d, want %d", got, want)
	}
	f, ok := n1.Usage.CPUOfCapacity()
	if !ok || f != 0.5 {
		t.Errorf("node cpu fraction = %v (%v), want 0.5", f, ok)
	}
}

func TestLabelSelectorNarrowsResults(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupPod, LabelSelector: "app=db",
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Rows) != 1 || snap.Rows[0].Name != "db-0" {
		t.Errorf("label selector did not narrow results: %v", names(snap.Rows))
	}
}

func TestNameFilterKeepsParentOfMatchingChild(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupWorkload, NamePattern: "abc123-bbbbb",
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Rows) != 1 || snap.Rows[0].Name != "web" {
		t.Fatalf("expected the web deployment to survive a pod-name filter; got %v", names(snap.Rows))
	}
}

func TestNameFilterAcceptsRegexAndSubstring(t *testing.T) {
	kc, mock := testCluster()
	c := New(kc, mock)

	re, err := c.Collect(context.Background(), Options{Namespace: "prod", GroupBy: GroupWorkload, NamePattern: "^(web|db)$"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(re.Rows) != 2 {
		t.Errorf("regex filter matched %v, want web and db", names(re.Rows))
	}

	// An unbalanced bracket is not a valid regex; it must degrade to a
	// substring search rather than erroring or emptying the table.
	sub, err := c.Collect(context.Background(), Options{Namespace: "prod", GroupBy: GroupWorkload, NamePattern: "we[b"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(sub.Rows) != 0 {
		t.Errorf("expected no substring match for 'we[b', got %v", names(sub.Rows))
	}
}

func TestGroupByDeploymentRestrictsKind(t *testing.T) {
	kc, mock := testCluster()
	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupDeployment,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Rows) != 1 || snap.Rows[0].Name != "web" {
		t.Errorf("group-by deployment returned %v, want only web", names(snap.Rows))
	}
}

func TestTerminatedPodsAreExcluded(t *testing.T) {
	kc, mock := testCluster()
	done := pod("prod", "migrate-xyz", "node-1", nil, nil, container("job", "100m", "", "", ""))
	done.Status.Phase = corev1.PodSucceeded
	if _, err := kc.CoreV1().Pods("prod").Create(context.Background(), done, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	snap, err := New(kc, mock).Collect(context.Background(), Options{
		Namespace: "prod", GroupBy: GroupPod, IncludeMissing: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if findRow(snap.Rows, "migrate-xyz") != nil {
		t.Error("completed pods should not appear; they consume nothing")
	}
}

func TestParseGroupByAcceptsKubectlAbbreviations(t *testing.T) {
	cases := map[string]GroupBy{
		"":            GroupWorkload,
		"deploy":      GroupDeployment,
		"sts":         GroupStatefulSet,
		"DS":          GroupDaemonSet,
		"pods":        GroupPod,
		" namespace ": GroupNamespace,
	}
	for in, want := range cases {
		got, err := ParseGroupBy(in)
		if err != nil {
			t.Errorf("ParseGroupBy(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseGroupBy(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseGroupBy("nonsense"); err == nil {
		t.Error("expected an error for an unknown group-by")
	}
}

func names(rows []*model.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}
