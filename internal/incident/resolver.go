package incident

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

type Resolver struct {
	client client.Client
}

func NewResolver(c client.Client) *Resolver {
	return &Resolver{client: c}
}

func (r *Resolver) ResolvePodScope(ctx context.Context, namespace, podName string) (rcav1alpha1.IncidentScope, []rcav1alpha1.AffectedResource, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, pod); err != nil {
		scope := rcav1alpha1.IncidentScope{
			Level:     ScopeLevelPod,
			Namespace: namespace,
			ResourceRef: &rcav1alpha1.IncidentObjectRef{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Namespace:  namespace,
				Name:       podName,
			},
		}
		return scope, []rcav1alpha1.AffectedResource{{Kind: "Pod", Namespace: namespace, Name: podName}}, fmt.Errorf("get pod: %w", err)
	}

	scope := rcav1alpha1.IncidentScope{
		Level:     ScopeLevelPod,
		Namespace: namespace,
		ResourceRef: &rcav1alpha1.IncidentObjectRef{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Pod",
			Namespace:  namespace,
			Name:       pod.Name,
			UID:        string(pod.UID),
		},
	}
	affected := []rcav1alpha1.AffectedResource{
		{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Pod",
			Namespace:  namespace,
			Name:       pod.Name,
			UID:        string(pod.UID),
		},
	}

	workloadRef := r.resolveTopOwner(ctx, pod)
	if workloadRef != nil {
		scope.Level = ScopeLevelWorkload
		scope.WorkloadRef = workloadRef
		affected = append(affected, rcav1alpha1.AffectedResource{
			APIVersion: workloadRef.APIVersion,
			Kind:       workloadRef.Kind,
			Namespace:  workloadRef.Namespace,
			Name:       workloadRef.Name,
			UID:        workloadRef.UID,
		})
	}

	if pod.Spec.NodeName != "" {
		affected = appendIfMissing(affected, rcav1alpha1.AffectedResource{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Node",
			Name:       pod.Spec.NodeName,
		})
	}

	return scope, affected, nil
}

func (r *Resolver) resolveTopOwner(ctx context.Context, pod *corev1.Pod) *rcav1alpha1.IncidentObjectRef {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil
	}

	switch owner.Kind {
	case "StatefulSet", "DaemonSet", "Job":
		return &rcav1alpha1.IncidentObjectRef{
			APIVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Namespace:  pod.Namespace,
			Name:       owner.Name,
			UID:        string(owner.UID),
		}
	case "ReplicaSet":
		rs := &appsv1.ReplicaSet{}
		if err := r.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, rs); err != nil {
			return &rcav1alpha1.IncidentObjectRef{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       "ReplicaSet",
				Namespace:  pod.Namespace,
				Name:       owner.Name,
				UID:        string(owner.UID),
			}
		}
		rsOwner := metav1.GetControllerOf(rs)
		if rsOwner != nil && rsOwner.Kind == "Deployment" {
			return &rcav1alpha1.IncidentObjectRef{
				APIVersion: rsOwner.APIVersion,
				Kind:       "Deployment",
				Namespace:  pod.Namespace,
				Name:       rsOwner.Name,
				UID:        string(rsOwner.UID),
			}
		}
		return &rcav1alpha1.IncidentObjectRef{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "ReplicaSet",
			Namespace:  pod.Namespace,
			Name:       rs.Name,
			UID:        string(rs.UID),
		}
	case "CronJob":
		return &rcav1alpha1.IncidentObjectRef{
			APIVersion: batchv1.SchemeGroupVersion.String(),
			Kind:       "CronJob",
			Namespace:  pod.Namespace,
			Name:       owner.Name,
			UID:        string(owner.UID),
		}
	default:
		return &rcav1alpha1.IncidentObjectRef{
			APIVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Namespace:  pod.Namespace,
			Name:       owner.Name,
			UID:        string(owner.UID),
		}
	}
}

func appendIfMissing(in []rcav1alpha1.AffectedResource, candidate rcav1alpha1.AffectedResource) []rcav1alpha1.AffectedResource {
	for _, item := range in {
		if item.Kind == candidate.Kind && item.Namespace == candidate.Namespace && item.Name == candidate.Name {
			return in
		}
	}
	return append(in, candidate)
}
