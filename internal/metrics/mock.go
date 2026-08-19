package metrics

import (
	"context"
	"hash/fnv"
	"math"
	"sync"
	"time"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Mock produces synthetic but plausible measurements.
//
// It exists for two reasons. Tests need deterministic numbers, and anyone
// evaluating the tool without a cluster (or demoing it) needs the watch view to
// visibly move. Both are served by deriving each sample from a per-container
// hash plus a slow sine wave over wall-clock time: repeatable given a fixed
// clock, but alive when the clock runs.
type Mock struct {
	mu sync.Mutex

	pods    []MockPod
	nodes   []MockNode
	volumes []VolumeSample

	// Now is injectable so tests can pin the waveform.
	Now func() time.Time
	// Jitter scales the oscillation; 0 makes every sample constant.
	Jitter float64
}

// MockPod describes a synthetic pod's baseline consumption.
type MockPod struct {
	Namespace string
	Name      string
	// Baseline maps container name to its steady-state consumption. Actual
	// samples oscillate around these values.
	Baseline map[string]model.Amounts
}

// MockNode describes a synthetic node's baseline consumption.
type MockNode struct {
	Name     string
	Baseline model.Amounts
}

// NewMock builds a provider over the given synthetic inventory.
func NewMock(pods []MockPod, nodes []MockNode, volumes []VolumeSample) *Mock {
	return &Mock{
		pods:    pods,
		nodes:   nodes,
		volumes: volumes,
		Now:     time.Now,
		Jitter:  0.25,
	}
}

// Name implements Provider.
func (m *Mock) Name() string { return "mock" }

// wobble returns a multiplier in roughly [1-jitter, 1+jitter] that varies
// smoothly with time and differs per seed, so two containers never move in
// lockstep and the table looks like a real cluster rather than a metronome.
func (m *Mock) wobble(seed string, now time.Time) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	phase := float64(h.Sum32()%1000) / 1000 * 2 * math.Pi
	// A ~40s period is slow enough to watch and fast enough to see.
	t := float64(now.UnixNano()) / float64(time.Second) / 40 * 2 * math.Pi
	return 1 + m.Jitter*math.Sin(t+phase)
}

func scale(a model.Amounts, f float64) model.Amounts {
	return model.Amounts{
		CPUMilli:     int64(float64(a.CPUMilli) * f),
		MemBytes:     int64(float64(a.MemBytes) * f),
		StorageBytes: a.StorageBytes,
	}
}

// PodMetrics implements Provider. The label selector is ignored because the
// mock has no label index; callers filter downstream against the inventory.
func (m *Mock) PodMetrics(_ context.Context, namespace, _ string) ([]PodSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()

	out := make([]PodSample, 0, len(m.pods))
	for _, p := range m.pods {
		if namespace != "" && p.Namespace != namespace {
			continue
		}
		s := PodSample{
			Namespace:  p.Namespace,
			Name:       p.Name,
			Containers: make(map[string]model.Amounts, len(p.Baseline)),
			Window:     30 * time.Second,
			Timestamp:  now,
		}
		for c, base := range p.Baseline {
			s.Containers[c] = scale(base, m.wobble(p.Namespace+"/"+p.Name+"/"+c, now))
		}
		out = append(out, s)
	}
	return out, nil
}

// NodeMetrics implements Provider.
func (m *Mock) NodeMetrics(_ context.Context) ([]NodeSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()

	out := make([]NodeSample, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, NodeSample{
			Name:      n.Name,
			Usage:     scale(n.Baseline, m.wobble("node/"+n.Name, now)),
			Window:    30 * time.Second,
			Timestamp: now,
		})
	}
	return out, nil
}

// VolumeMetrics implements VolumeProvider. Volume usage creeps upward rather
// than oscillating, because that is how disks actually behave and it makes the
// threshold alerting path demonstrable.
func (m *Mock) VolumeMetrics(_ context.Context, namespace string) ([]VolumeSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()

	out := make([]VolumeSample, 0, len(m.volumes))
	for _, v := range m.volumes {
		if namespace != "" && v.Namespace != namespace {
			continue
		}
		v.Timestamp = now
		if v.CapacityBytes > 0 {
			creep := 1 + 0.02*math.Sin(float64(now.Unix())/300)
			used := int64(float64(v.UsedBytes) * creep)
			if used > v.CapacityBytes {
				used = v.CapacityBytes
			}
			v.UsedBytes = used
		}
		out = append(out, v)
	}
	return out, nil
}

var _ Provider = (*Mock)(nil)
var _ VolumeProvider = (*Mock)(nil)
