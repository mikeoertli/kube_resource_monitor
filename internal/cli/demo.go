package cli

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mikeoertli/kube_resource_monitor/internal/metrics"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// demoCluster builds a synthetic cluster.
//
// This exists so the tool is evaluable, demoable, and debuggable with no
// cluster at all: `krm --demo` gives a live, moving view. The shape is chosen
// to exercise the interesting cases rather than to look tidy -- a workload with
// no limits, a container running over its limit, a pod with no metrics yet, a
// nearly-full volume.
func demoCluster() (*fake.Clientset, *metrics.Mock) {
	q := resource.MustParse

	ctr := func(name, cpuReq, memReq, cpuLim, memLim string) corev1.Container {
		c := corev1.Container{Name: name, Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{},
		}}
		if cpuReq != "" {
			c.Resources.Requests[corev1.ResourceCPU] = q(cpuReq)
		}
		if memReq != "" {
			c.Resources.Requests[corev1.ResourceMemory] = q(memReq)
		}
		if cpuLim != "" {
			c.Resources.Limits[corev1.ResourceCPU] = q(cpuLim)
		}
		if memLim != "" {
			c.Resources.Limits[corev1.ResourceMemory] = q(memLim)
		}
		return c
	}
	ctrl := func(kind, name string) metav1.OwnerReference {
		t := true
		return metav1.OwnerReference{Kind: kind, Name: name, Controller: &t}
	}
	mkPod := func(ns, name, node string, owner *metav1.OwnerReference, labels map[string]string, cs ...corev1.Container) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns, Name: name, Labels: labels,
				CreationTimestamp: metav1.Now(),
			},
			Spec:   corev1.PodSpec{NodeName: node, Containers: cs},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
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

	var objs []runtimeObject

	rs := func(ns, name, dep string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name, OwnerReferences: []metav1.OwnerReference{ctrl("Deployment", dep)},
		}}
	}
	dep := func(ns, name string, replicas, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
		}
	}

	objs = append(objs,
		dep("prod", "storefront", 3, 3), rs("prod", "storefront-7c9d", "storefront"),
		dep("prod", "checkout-api", 2, 2), rs("prod", "checkout-api-5f2a", "checkout-api"),
		dep("prod", "image-resizer", 1, 1), rs("prod", "image-resizer-91bc", "image-resizer"),
	)

	sfOwner := ctrl("ReplicaSet", "storefront-7c9d")
	coOwner := ctrl("ReplicaSet", "checkout-api-5f2a")
	irOwner := ctrl("ReplicaSet", "image-resizer-91bc")
	dbOwner := ctrl("StatefulSet", "postgres")
	nodeExporterOwner := ctrl("DaemonSet", "node-exporter")

	nodes := []string{"worker-1", "worker-2", "worker-3"}
	for i, suffix := range []string{"h4k2n", "p8w3d", "z1x9c"} {
		objs = append(objs, mkPod("prod", "storefront-7c9d-"+suffix, nodes[i%3], &sfOwner,
			map[string]string{"app": "storefront", "tier": "frontend"},
			ctr("web", "200m", "256Mi", "1", "512Mi"),
			ctr("envoy", "50m", "64Mi", "200m", "128Mi")))
	}
	for i, suffix := range []string{"a1b2c", "d3e4f"} {
		objs = append(objs, mkPod("prod", "checkout-api-5f2a-"+suffix, nodes[i%3], &coOwner,
			map[string]string{"app": "checkout-api", "tier": "backend"},
			ctr("api", "500m", "512Mi", "2", "1Gi")))
	}
	// No limits at all: the percentage columns must degrade gracefully.
	objs = append(objs, mkPod("prod", "image-resizer-91bc-m7n8p", "worker-2", &irOwner,
		map[string]string{"app": "image-resizer"},
		ctr("resizer", "250m", "", "", "")))

	objs = append(objs,
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "postgres"},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptr32(2)},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: 2},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "node-exporter"},
			Status:     appsv1.DaemonSetStatus{NumberReady: 3, DesiredNumberScheduled: 3},
		},
	)
	for i := 0; i < 2; i++ {
		name := "postgres-" + itoa(i)
		p := mkPod("prod", name, nodes[i], &dbOwner, map[string]string{"app": "postgres"},
			ctr("postgres", "1", "4Gi", "2", "8Gi"))
		p.Spec.Volumes = []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: "data-" + name,
			}},
		}}
		objs = append(objs, p)
	}
	for i, n := range nodes {
		_ = i
		objs = append(objs, mkPod("monitoring", "node-exporter-"+n, n, &nodeExporterOwner,
			map[string]string{"app": "node-exporter"},
			ctr("exporter", "50m", "64Mi", "100m", "128Mi")))
	}
	// A pod with no owner and no metrics sample yet.
	objs = append(objs, mkPod("prod", "debug-shell", "worker-3", nil, nil, ctr("shell", "", "", "", "")))

	for _, n := range nodes {
		objs = append(objs, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: n},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: q("8"), corev1.ResourceMemory: q("32Gi"),
			}},
		})
	}

	pvc := func(ns, name, size string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: metav1.Now()},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: q(size)},
			}},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: q(size)},
			},
		}
	}
	objs = append(objs,
		pvc("prod", "data-postgres-0", "100Gi"),
		pvc("prod", "data-postgres-1", "100Gi"),
		pvc("prod", "uploads", "50Gi"),
	)

	kc := fake.NewSimpleClientset(toRuntimeObjects(objs)...)

	mb := func(n int64) int64 { return n << 20 }
	gb := func(n int64) int64 { return n << 30 }

	mockPods := []metrics.MockPod{
		{Namespace: "prod", Name: "storefront-7c9d-h4k2n", Baseline: map[string]model.Amounts{
			"web": {CPUMilli: 420, MemBytes: mb(310)}, "envoy": {CPUMilli: 60, MemBytes: mb(70)}}},
		{Namespace: "prod", Name: "storefront-7c9d-p8w3d", Baseline: map[string]model.Amounts{
			"web": {CPUMilli: 880, MemBytes: mb(470)}, "envoy": {CPUMilli: 75, MemBytes: mb(90)}}},
		{Namespace: "prod", Name: "storefront-7c9d-z1x9c", Baseline: map[string]model.Amounts{
			"web": {CPUMilli: 300, MemBytes: mb(240)}, "envoy": {CPUMilli: 45, MemBytes: mb(60)}}},
		{Namespace: "prod", Name: "checkout-api-5f2a-a1b2c", Baseline: map[string]model.Amounts{
			"api": {CPUMilli: 1750, MemBytes: mb(980)}}},
		{Namespace: "prod", Name: "checkout-api-5f2a-d3e4f", Baseline: map[string]model.Amounts{
			"api": {CPUMilli: 640, MemBytes: mb(600)}}},
		{Namespace: "prod", Name: "image-resizer-91bc-m7n8p", Baseline: map[string]model.Amounts{
			"resizer": {CPUMilli: 2200, MemBytes: gb(3)}}},
		{Namespace: "prod", Name: "postgres-0", Baseline: map[string]model.Amounts{
			"postgres": {CPUMilli: 1400, MemBytes: gb(6)}}},
		{Namespace: "prod", Name: "postgres-1", Baseline: map[string]model.Amounts{
			"postgres": {CPUMilli: 300, MemBytes: gb(4)}}},
	}
	for _, n := range nodes {
		mockPods = append(mockPods, metrics.MockPod{
			Namespace: "monitoring", Name: "node-exporter-" + n,
			Baseline: map[string]model.Amounts{"exporter": {CPUMilli: 25, MemBytes: mb(40)}},
		})
	}

	mockNodes := []metrics.MockNode{
		{Name: "worker-1", Baseline: model.Amounts{CPUMilli: 2600, MemBytes: gb(12)}},
		{Name: "worker-2", Baseline: model.Amounts{CPUMilli: 4100, MemBytes: gb(19)}},
		{Name: "worker-3", Baseline: model.Amounts{CPUMilli: 1200, MemBytes: gb(7)}},
	}

	volumes := []metrics.VolumeSample{
		{Namespace: "prod", ClaimName: "data-postgres-0", UsedBytes: gb(93), CapacityBytes: gb(100), MountedByPod: "postgres-0"},
		{Namespace: "prod", ClaimName: "data-postgres-1", UsedBytes: gb(41), CapacityBytes: gb(100), MountedByPod: "postgres-1"},
		{Namespace: "prod", ClaimName: "uploads", UsedBytes: gb(12), CapacityBytes: gb(50)},
	}

	return kc, metrics.NewMock(mockPods, mockNodes, volumes)
}

func ptr32(i int32) *int32 { return &i }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
