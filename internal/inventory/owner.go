package inventory

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/mikeoertli/kube_resource_monitor/internal/model"
)

// Owner is the workload a pod ultimately belongs to.
type Owner struct {
	Kind      model.Kind
	Name      string
	Namespace string
}

// Key uniquely identifies an owner for grouping.
func (o Owner) Key() string { return string(o.Kind) + "/" + o.Namespace + "/" + o.Name }

// ownerResolver walks ownerReferences up to the top-level workload.
//
// The indirection matters: a pod's controller is a ReplicaSet, not the
// Deployment anyone actually thinks in terms of, and a pod from a CronJob is
// owned by a Job. Resolving one extra hop turns "web-7d9f8b6c5-x2k4p" into
// "web", which is the unit people scale, alert on, and page about.
type ownerResolver struct {
	kube kubernetes.Interface

	// rsOwners maps "namespace/replicaset" to its controlling Deployment.
	rsOwners map[string]Owner
	// jobOwners maps "namespace/job" to its controlling CronJob.
	jobOwners map[string]Owner
}

func newOwnerResolver(k kubernetes.Interface) *ownerResolver {
	return &ownerResolver{
		kube:      k,
		rsOwners:  map[string]Owner{},
		jobOwners: map[string]Owner{},
	}
}

// prime does the two list calls needed to resolve every pod's grandparent.
//
// Listing all ReplicaSets and Jobs once is dramatically cheaper than a GET per
// pod: a namespace with 300 pods might have 20 ReplicaSets, and watch mode
// repeats this every refresh interval.
func (r *ownerResolver) prime(ctx context.Context, namespace string) error {
	rsList, err := r.kube.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing replicasets: %w", err)
	}
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if c := metav1.GetControllerOf(rs); c != nil {
			r.rsOwners[rs.Namespace+"/"+rs.Name] = Owner{
				Kind:      model.Kind(c.Kind),
				Name:      c.Name,
				Namespace: rs.Namespace,
			}
		}
	}

	jobList, err := r.kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Jobs are less universally readable than ReplicaSets, and a cluster
		// with no Job RBAC is still perfectly monitorable; degrade instead of
		// failing the whole collection.
		return nil
	}
	for i := range jobList.Items {
		j := &jobList.Items[i]
		if c := metav1.GetControllerOf(j); c != nil {
			r.jobOwners[j.Namespace+"/"+j.Name] = Owner{
				Kind:      model.Kind(c.Kind),
				Name:      c.Name,
				Namespace: j.Namespace,
			}
		}
	}
	return nil
}

// resolve returns the top-level workload owning a pod.
//
// Pods with no controller (a bare `kubectl run`, or a static mirror pod) come
// back as KindStandalone named after the pod itself, so they still get a row
// instead of silently vanishing from a workload-grouped view.
func (r *ownerResolver) resolve(pod *corev1.Pod) Owner {
	ctrl := metav1.GetControllerOf(pod)
	if ctrl == nil {
		return Owner{Kind: model.KindStandalone, Name: pod.Name, Namespace: pod.Namespace}
	}

	switch ctrl.Kind {
	case "ReplicaSet":
		if o, ok := r.rsOwners[pod.Namespace+"/"+ctrl.Name]; ok && o.Kind == model.KindDeployment {
			return o
		}
		// A ReplicaSet with no Deployment above it is a legitimate, if rare,
		// way to run workloads; report it as itself.
		return Owner{Kind: model.KindReplicaSet, Name: ctrl.Name, Namespace: pod.Namespace}
	case "Job":
		if o, ok := r.jobOwners[pod.Namespace+"/"+ctrl.Name]; ok && o.Kind == model.KindCronJob {
			return o
		}
		return Owner{Kind: model.KindJob, Name: ctrl.Name, Namespace: pod.Namespace}
	default:
		return Owner{Kind: model.Kind(ctrl.Kind), Name: ctrl.Name, Namespace: pod.Namespace}
	}
}
