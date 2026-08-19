package inventory

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// declared captures a container's requests and limits along with whether each
// was actually specified, since an unspecified limit and a limit of zero are
// very different things.
type declared struct {
	Requests    model.Amounts
	Limits      model.Amounts
	HasCPUReq   bool
	HasMemReq   bool
	HasCPULimit bool
	HasMemLimit bool
}

func (d declared) add(o declared) declared {
	return declared{
		Requests:    d.Requests.Add(o.Requests),
		Limits:      d.Limits.Add(o.Limits),
		HasCPUReq:   d.HasCPUReq || o.HasCPUReq,
		HasMemReq:   d.HasMemReq || o.HasMemReq,
		HasCPULimit: d.HasCPULimit || o.HasCPULimit,
		HasMemLimit: d.HasMemLimit || o.HasMemLimit,
	}
}

func maxDeclared(a, b declared) declared {
	out := declared{
		HasCPUReq:   a.HasCPUReq || b.HasCPUReq,
		HasMemReq:   a.HasMemReq || b.HasMemReq,
		HasCPULimit: a.HasCPULimit || b.HasCPULimit,
		HasMemLimit: a.HasMemLimit || b.HasMemLimit,
	}
	out.Requests.CPUMilli = max64(a.Requests.CPUMilli, b.Requests.CPUMilli)
	out.Requests.MemBytes = max64(a.Requests.MemBytes, b.Requests.MemBytes)
	out.Limits.CPUMilli = max64(a.Limits.CPUMilli, b.Limits.CPUMilli)
	out.Limits.MemBytes = max64(a.Limits.MemBytes, b.Limits.MemBytes)
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// containerResources reads one container's declared requests and limits.
func containerResources(c *corev1.Container) declared {
	var d declared
	if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
		d.Requests.CPUMilli = q.MilliValue()
		d.HasCPUReq = true
	}
	if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
		d.Requests.MemBytes = q.Value()
		d.HasMemReq = true
	}
	if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		d.Limits.CPUMilli = q.MilliValue()
		d.HasCPULimit = true
	}
	if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
		d.Limits.MemBytes = q.Value()
		d.HasMemLimit = true
	}
	return d
}

// isSidecar reports whether an init container is a restartable "sidecar",
// which runs for the pod's whole lifetime and therefore adds to the pod's
// resource footprint rather than merely peaking during startup.
func isSidecar(c *corev1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// effectivePodResources computes the pod's requests and limits the way the
// scheduler does.
//
// This is deliberately not a plain sum over containers. Kubernetes defines the
// effective pod request as the larger of:
//
//	(a) the sum of regular containers plus all restartable init containers, and
//	(b) for each non-restartable init container, that container's request plus
//	    the sum of restartable init containers declared before it
//
// because ordinary init containers run to completion one at a time before the
// app starts, so their footprint overlaps with nothing except sidecars already
// started. Summing everything would overstate a pod that runs a heavyweight
// migration init container, sometimes by a lot. Pod-level Overhead from the
// RuntimeClass is added on top.
func effectivePodResources(spec *corev1.PodSpec) declared {
	var regular declared
	for i := range spec.Containers {
		regular = regular.add(containerResources(&spec.Containers[i]))
	}

	var sidecars declared
	var initPeak declared
	running := declared{}
	for i := range spec.InitContainers {
		c := &spec.InitContainers[i]
		d := containerResources(c)
		if isSidecar(c) {
			sidecars = sidecars.add(d)
			running = running.add(d)
			continue
		}
		// A plain init container's peak is itself plus every sidecar started
		// before it.
		initPeak = maxDeclared(initPeak, running.add(d))
	}

	steady := regular.add(sidecars)
	out := maxDeclared(steady, initPeak)

	// Pod-level resources (the PodLevelResources feature) override the derived
	// values when the spec sets them explicitly.
	if spec.Resources != nil {
		var pod declared
		if q, ok := spec.Resources.Requests[corev1.ResourceCPU]; ok {
			pod.Requests.CPUMilli = q.MilliValue()
			pod.HasCPUReq = true
		}
		if q, ok := spec.Resources.Requests[corev1.ResourceMemory]; ok {
			pod.Requests.MemBytes = q.Value()
			pod.HasMemReq = true
		}
		if q, ok := spec.Resources.Limits[corev1.ResourceCPU]; ok {
			pod.Limits.CPUMilli = q.MilliValue()
			pod.HasCPULimit = true
		}
		if q, ok := spec.Resources.Limits[corev1.ResourceMemory]; ok {
			pod.Limits.MemBytes = q.Value()
			pod.HasMemLimit = true
		}
		if pod.HasCPUReq {
			out.Requests.CPUMilli, out.HasCPUReq = pod.Requests.CPUMilli, true
		}
		if pod.HasMemReq {
			out.Requests.MemBytes, out.HasMemReq = pod.Requests.MemBytes, true
		}
		if pod.HasCPULimit {
			out.Limits.CPUMilli, out.HasCPULimit = pod.Limits.CPUMilli, true
		}
		if pod.HasMemLimit {
			out.Limits.MemBytes, out.HasMemLimit = pod.Limits.MemBytes, true
		}
	}

	for name, q := range spec.Overhead {
		switch name {
		case corev1.ResourceCPU:
			out.Requests.CPUMilli += q.MilliValue()
			if out.HasCPULimit {
				out.Limits.CPUMilli += q.MilliValue()
			}
		case corev1.ResourceMemory:
			out.Requests.MemBytes += q.Value()
			if out.HasMemLimit {
				out.Limits.MemBytes += q.Value()
			}
		}
	}
	return out
}

// applyDeclared copies a declared bundle onto a model.Usage.
func applyDeclared(u *model.Usage, d declared) {
	u.Requests = d.Requests
	u.Limits = d.Limits
	u.HasCPURequest = d.HasCPUReq
	u.HasMemRequest = d.HasMemReq
	u.HasCPULimit = d.HasCPULimit
	u.HasMemLimit = d.HasMemLimit
}
