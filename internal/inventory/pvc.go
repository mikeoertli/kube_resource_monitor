package inventory

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mikeoertli/kube_resource_monitor/internal/metrics"
	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// collectPVC builds volume rows.
//
// Two sources are joined here. The PVC objects themselves give the provisioned
// capacity, storage class, phase, and which pods mount them -- all readable with
// ordinary namespace permissions. Actual bytes used come only from the kubelet
// summary endpoint, which needs nodes/proxy access that many users lack.
//
// The split matters for the user experience: without node proxy access you
// still get a complete inventory of claims and their sizes, just with usage
// shown as unknown, plus a warning explaining exactly why. That is far more
// useful than an error.
func (c *Collector) collectPVC(ctx context.Context, opts Options, snap *Snapshot) (*Snapshot, error) {
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing persistent volume claims: %w", err)
	}

	usage := map[string]metrics.VolumeSample{}
	if vp, ok := c.provider.(metrics.VolumeProvider); ok {
		samples, err := vp.VolumeMetrics(ctx, opts.Namespace)
		if err != nil {
			snap.Warnings = append(snap.Warnings,
				"volume usage unavailable ("+err.Error()+"); showing provisioned capacity only")
		}
		for _, s := range samples {
			// A claim mounted by several pods is reported once per pod; the
			// numbers describe the same filesystem, so last write wins rather
			// than summing (which would multiply usage by the mount count).
			usage[s.Namespace+"/"+s.ClaimName] = s
		}
	} else {
		snap.Warnings = append(snap.Warnings, "this metrics source cannot report volume usage; showing capacity only")
	}

	mountedBy := c.podsMountingClaims(ctx, opts.Namespace)

	rows := make([]*model.Row, 0, len(claims.Items))
	for i := range claims.Items {
		pvc := &claims.Items[i]
		row := &model.Row{
			Kind:          model.KindPVC,
			Name:          pvc.Name,
			Namespace:     pvc.Namespace,
			Labels:        pvc.Labels,
			Phase:         string(pvc.Status.Phase),
			Authoritative: true,
			Usage:         model.Usage{PreferCapacity: true},
		}
		if !pvc.CreationTimestamp.IsZero() {
			row.Age = time.Since(pvc.CreationTimestamp.Time)
		}
		// Status.Capacity is what was actually provisioned, which can exceed
		// the request; fall back to the request for a claim still binding.
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			row.Usage.Capacity.StorageBytes = q.Value()
		} else if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			row.Usage.Capacity.StorageBytes = q.Value()
		}
		if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			row.Usage.Requests.StorageBytes = q.Value()
		}

		if s, ok := usage[pvc.Namespace+"/"+pvc.Name]; ok {
			row.Usage.Used.StorageBytes = s.UsedBytes
			row.Usage.UsedKnown = true
			// The filesystem's own view of capacity is more accurate than the
			// requested size once formatting overhead is accounted for.
			if s.CapacityBytes > 0 {
				row.Usage.Capacity.StorageBytes = s.CapacityBytes
			}
			row.Node = s.MountedByPod
		} else {
			row.MetricsMissing = true
		}

		if pods := mountedBy[pvc.Namespace+"/"+pvc.Name]; len(pods) > 0 {
			row.Ready = fmt.Sprintf("%d pod", len(pods))
			if len(pods) > 1 {
				row.Ready += "s"
			}
			if row.Node == "" {
				row.Node = pods[0]
			}
		} else {
			// An unmounted claim is still billed for and still counts against
			// quota, so it is worth calling out rather than hiding.
			row.Ready = "unmounted"
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})

	snap.Rows = filterByName(rows, opts.NamePattern)
	if opts.OnlyProblems {
		snap.Rows = filterProblems(snap.Rows, opts.ProblemThreshold)
	}
	snap.Totals = model.TotalOf(snap.Rows)
	return snap, nil
}

// podsMountingClaims maps "namespace/claim" to the pods that reference it.
func (c *Collector) podsMountingClaims(ctx context.Context, namespace string) map[string][]string {
	out := map[string][]string{}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			key := p.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
			out[key] = append(out[key], p.Name)
		}
	}
	return out
}
