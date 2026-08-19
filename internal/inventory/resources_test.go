package inventory

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func always() *corev1.ContainerRestartPolicy {
	p := corev1.ContainerRestartPolicyAlways
	return &p
}

func TestEffectivePodResourcesSumsRegularContainers(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{
		container("app", "100m", "128Mi", "500m", "512Mi"),
		container("sidecar", "50m", "64Mi", "200m", "256Mi"),
	}}
	d := effectivePodResources(spec)

	if got, want := d.Requests.CPUMilli, int64(150); got != want {
		t.Errorf("cpu request = %d, want %d", got, want)
	}
	if got, want := d.Limits.CPUMilli, int64(700); got != want {
		t.Errorf("cpu limit = %d, want %d", got, want)
	}
	if !d.HasCPUReq || !d.HasMemLimit {
		t.Error("presence flags should be set when values were declared")
	}
}

// A one-shot init container runs to completion before the app starts, so its
// footprint overlaps with nothing. Summing it into the pod's request would
// permanently overstate a pod that runs a heavyweight migration at startup.
func TestEffectivePodResourcesTakesMaxOverInitContainers(t *testing.T) {
	spec := &corev1.PodSpec{
		InitContainers: []corev1.Container{container("migrate", "2", "4Gi", "2", "4Gi")},
		Containers:     []corev1.Container{container("app", "100m", "128Mi", "500m", "512Mi")},
	}
	d := effectivePodResources(spec)

	// max(app=100m, migrate=2000m) rather than the 2100m a naive sum gives.
	if got, want := d.Requests.CPUMilli, int64(2000); got != want {
		t.Errorf("cpu request = %d, want %d (max, not sum)", got, want)
	}
	if got, want := d.Requests.MemBytes, int64(4<<30); got != want {
		t.Errorf("mem request = %d, want %d", got, want)
	}
}

// A restartable init container is a sidecar: it keeps running alongside the
// app, so it genuinely adds to the steady-state footprint.
func TestEffectivePodResourcesAddsSidecars(t *testing.T) {
	proxy := container("proxy", "200m", "256Mi", "400m", "512Mi")
	proxy.RestartPolicy = always()
	spec := &corev1.PodSpec{
		InitContainers: []corev1.Container{proxy},
		Containers:     []corev1.Container{container("app", "100m", "128Mi", "500m", "512Mi")},
	}
	d := effectivePodResources(spec)

	if got, want := d.Requests.CPUMilli, int64(300); got != want {
		t.Errorf("cpu request = %d, want %d (sidecar adds to the app)", got, want)
	}
	if got, want := d.Limits.CPUMilli, int64(900); got != want {
		t.Errorf("cpu limit = %d, want %d", got, want)
	}
}

// The peak of a plain init container includes any sidecar already started
// before it, since both are running at that moment.
func TestEffectivePodResourcesCountsSidecarDuringInit(t *testing.T) {
	proxy := container("proxy", "200m", "", "", "")
	proxy.RestartPolicy = always()
	spec := &corev1.PodSpec{
		InitContainers: []corev1.Container{proxy, container("migrate", "1", "", "", "")},
		Containers:     []corev1.Container{container("app", "100m", "", "", "")},
	}
	d := effectivePodResources(spec)

	// max(app+proxy = 300m, migrate+proxy = 1200m)
	if got, want := d.Requests.CPUMilli, int64(1200); got != want {
		t.Errorf("cpu request = %d, want %d", got, want)
	}
}

func TestEffectivePodResourcesAddsOverhead(t *testing.T) {
	spec := &corev1.PodSpec{
		Containers: []corev1.Container{container("app", "100m", "128Mi", "500m", "512Mi")},
		Overhead:   corev1.ResourceList{corev1.ResourceCPU: qty("50m"), corev1.ResourceMemory: qty("32Mi")},
	}
	d := effectivePodResources(spec)

	if got, want := d.Requests.CPUMilli, int64(150); got != want {
		t.Errorf("cpu request = %d, want %d (overhead included)", got, want)
	}
	if got, want := d.Limits.CPUMilli, int64(550); got != want {
		t.Errorf("cpu limit = %d, want %d (overhead included)", got, want)
	}
}

func TestUndeclaredResourcesLeaveFlagsUnset(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{
		{Name: "bare"}, // no Resources at all
	}}
	d := effectivePodResources(spec)

	if d.HasCPUReq || d.HasMemReq || d.HasCPULimit || d.HasMemLimit {
		t.Error("a container with no resource block must not claim declared values")
	}
	if d.Requests.CPUMilli != 0 || d.Limits.MemBytes != 0 {
		t.Error("undeclared resources should be zero")
	}
}
