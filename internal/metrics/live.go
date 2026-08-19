package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Live reads from metrics.k8s.io.
//
// Talking to the typed API instead of shelling out to kubectl avoids a
// subprocess per refresh and, more importantly, avoids parsing a
// human-formatted table whose column set has changed across kubectl releases.
type Live struct {
	metrics metricsv.Interface
	kube    kubernetes.Interface
}

// NewLive builds a provider over the given clients.
func NewLive(m metricsv.Interface, k kubernetes.Interface) *Live {
	return &Live{metrics: m, kube: k}
}

// Name implements Provider.
func (l *Live) Name() string { return "metrics.k8s.io" }

// PodMetrics implements Provider.
func (l *Live) PodMetrics(ctx context.Context, namespace, labelSelector string) ([]PodSample, error) {
	list, err := l.metrics.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pod metrics: %w", err)
	}
	out := make([]PodSample, 0, len(list.Items))
	for i := range list.Items {
		pm := &list.Items[i]
		s := PodSample{
			Namespace:  pm.Namespace,
			Name:       pm.Name,
			Containers: make(map[string]model.Amounts, len(pm.Containers)),
			Window:     pm.Window.Duration,
			Timestamp:  pm.Timestamp.Time,
		}
		for j := range pm.Containers {
			c := &pm.Containers[j]
			cpu := c.Usage.Cpu()
			mem := c.Usage.Memory()
			s.Containers[c.Name] = model.Amounts{
				CPUMilli: model.CPUFromQuantity(cpu),
				MemBytes: model.BytesFromQuantity(mem),
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// NodeMetrics implements Provider.
func (l *Live) NodeMetrics(ctx context.Context) ([]NodeSample, error) {
	list, err := l.metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing node metrics: %w", err)
	}
	out := make([]NodeSample, 0, len(list.Items))
	for i := range list.Items {
		nm := &list.Items[i]
		out = append(out, NodeSample{
			Name: nm.Name,
			Usage: model.Amounts{
				CPUMilli: model.CPUFromQuantity(nm.Usage.Cpu()),
				MemBytes: model.BytesFromQuantity(nm.Usage.Memory()),
			},
			Window:    nm.Window.Duration,
			Timestamp: nm.Timestamp.Time,
		})
	}
	return out, nil
}

// kubelet summary API types. Only the fields we need are declared; the real
// payload is large and mostly irrelevant here.
type summaryResponse struct {
	Node struct {
		NodeName string `json:"nodeName"`
	} `json:"node"`
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		Volume []struct {
			Time           time.Time `json:"time"`
			AvailableBytes *int64    `json:"availableBytes"`
			CapacityBytes  *int64    `json:"capacityBytes"`
			UsedBytes      *int64    `json:"usedBytes"`
			InodesUsed     *int64    `json:"inodesUsed"`
			InodesFree     *int64    `json:"inodesFree"`
			Name           string    `json:"name"`
			PVCRef         *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

// VolumeMetrics implements VolumeProvider by scraping every kubelet's summary
// endpoint through the apiserver's node proxy.
//
// There is no aggregated API for volume usage: the kubelet that has a volume
// mounted is the only component that has stat'd the filesystem. We therefore
// fan out across nodes, and tolerate per-node failures because a single
// unreachable or restarting kubelet should not blank out the whole view. That
// also means results can be partially complete, which is why the returned
// error is only non-nil when *every* node failed.
func (l *Live) VolumeMetrics(ctx context.Context, namespace string) ([]VolumeSample, error) {
	nodes, err := l.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes for volume stats: %w", err)
	}
	if len(nodes.Items) == 0 {
		return nil, nil
	}

	type result struct {
		samples []VolumeSample
		err     error
	}
	results := make([]result, len(nodes.Items))

	var wg sync.WaitGroup
	// A modest cap keeps a 500-node cluster from opening 500 simultaneous
	// proxy connections through the apiserver.
	sem := make(chan struct{}, 8)
	for i := range nodes.Items {
		wg.Add(1)
		go func(i int, node corev1.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := l.nodeVolumeStats(ctx, node.Name, namespace)
			results[i] = result{samples: s, err: err}
		}(i, nodes.Items[i])
	}
	wg.Wait()

	var out []VolumeSample
	var firstErr error
	failures := 0
	for _, r := range results {
		if r.err != nil {
			failures++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		out = append(out, r.samples...)
	}
	if failures == len(nodes.Items) {
		return nil, fmt.Errorf("volume stats unavailable on all %d nodes (needs nodes/proxy access): %w", failures, firstErr)
	}
	return out, nil
}

func (l *Live) nodeVolumeStats(ctx context.Context, nodeName, namespace string) ([]VolumeSample, error) {
	raw, err := l.kube.CoreV1().RESTClient().Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("stats", "summary").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("node %s summary: %w", nodeName, err)
	}
	var sum summaryResponse
	if err := json.Unmarshal(raw, &sum); err != nil {
		return nil, fmt.Errorf("node %s summary decode: %w", nodeName, err)
	}

	var out []VolumeSample
	for _, p := range sum.Pods {
		for _, v := range p.Volume {
			// Only PVC-backed volumes are interesting; configmap, secret, and
			// emptyDir mounts would drown the view in noise.
			if v.PVCRef == nil {
				continue
			}
			if namespace != "" && v.PVCRef.Namespace != namespace {
				continue
			}
			s := VolumeSample{
				Namespace:    v.PVCRef.Namespace,
				ClaimName:    v.PVCRef.Name,
				MountedByPod: p.PodRef.Name,
				Timestamp:    v.Time,
			}
			if v.UsedBytes != nil {
				s.UsedBytes = *v.UsedBytes
			}
			if v.CapacityBytes != nil {
				s.CapacityBytes = *v.CapacityBytes
			}
			if v.InodesUsed != nil {
				s.InodesUsed = *v.InodesUsed
			}
			if v.InodesFree != nil {
				s.InodesFree = *v.InodesFree
			}
			out = append(out, s)
		}
	}
	return out, nil
}

var _ Provider = (*Live)(nil)
var _ VolumeProvider = (*Live)(nil)
