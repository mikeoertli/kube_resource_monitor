// Package metrics sources live resource consumption.
//
// Everything behind the Provider interface so the TUI, the text renderer, and
// the tests all consume the same shape of data whether it came from a real
// cluster or a fixture.
package metrics

import (
	"context"
	"time"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// PodSample is one pod's measured consumption, broken out per container.
//
// Containers are kept separate rather than pre-summed because container-level
// detail is a first-class view, and summing is cheap while un-summing is
// impossible.
type PodSample struct {
	Namespace  string
	Name       string
	Containers map[string]model.Amounts
	// Window is the interval the metrics-server averaged over, typically ~30s.
	// Surfacing it stops users from reading a 30-second average as an instant.
	Window    time.Duration
	Timestamp time.Time
}

// Total sums the pod's containers.
func (p PodSample) Total() model.Amounts {
	var out model.Amounts
	for _, c := range p.Containers {
		out = out.Add(c)
	}
	return out
}

// NodeSample is one node's measured consumption.
type NodeSample struct {
	Name      string
	Usage     model.Amounts
	Window    time.Duration
	Timestamp time.Time
}

// VolumeSample is one PersistentVolumeClaim's measured consumption, as reported
// by the kubelet that has it mounted.
type VolumeSample struct {
	Namespace string
	// ClaimName is the PVC name, which is how users think about volumes even
	// though the kubelet reports them per-pod-per-volume.
	ClaimName string
	UsedBytes int64
	// CapacityBytes is what the filesystem reports, which can differ slightly
	// from the PVC's requested size after filesystem overhead.
	CapacityBytes int64
	InodesUsed    int64
	InodesFree    int64
	MountedByPod  string
	Timestamp     time.Time
}

// Provider supplies measurements. Implementations must be safe for concurrent
// use, because watch mode fires refreshes on a timer while the UI reads.
type Provider interface {
	// Name identifies the provider in status lines ("metrics.k8s.io", "mock").
	Name() string
	// PodMetrics returns samples for pods in namespace (empty means all),
	// optionally narrowed by a label selector.
	PodMetrics(ctx context.Context, namespace, labelSelector string) ([]PodSample, error)
	// NodeMetrics returns samples for every node.
	NodeMetrics(ctx context.Context) ([]NodeSample, error)
}

// VolumeProvider is implemented by providers that can also report PVC usage.
//
// It is a separate interface because volume stats come from a different place
// (the kubelet summary endpoint, not metrics.k8s.io) and require node/proxy
// permissions many users will not have. Callers type-assert and degrade
// gracefully rather than failing the whole refresh.
type VolumeProvider interface {
	VolumeMetrics(ctx context.Context, namespace string) ([]VolumeSample, error)
}

// PodSampleIndex keys samples by namespace/name for O(1) joins against the pod
// inventory.
type PodSampleIndex map[string]PodSample

// IndexPods builds a lookup keyed "namespace/name".
func IndexPods(samples []PodSample) PodSampleIndex {
	idx := make(PodSampleIndex, len(samples))
	for _, s := range samples {
		idx[s.Namespace+"/"+s.Name] = s
	}
	return idx
}

// Get looks up a sample by namespace and name.
func (i PodSampleIndex) Get(namespace, name string) (PodSample, bool) {
	s, ok := i[namespace+"/"+name]
	return s, ok
}
