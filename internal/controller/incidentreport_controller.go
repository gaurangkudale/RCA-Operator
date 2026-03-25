/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

const (
	stabilizationDelay         = 5 * time.Minute
	healthyResolveWindow       = 5 * time.Minute
	incidentAnnotationLastSeen = "rca.rca-operator.tech/last-seen"

	incidentTypeNodeFailure = "NodeFailure"
	maxTimelineEntriesCtrl  = 50
	resourceKindPod         = "Pod"
)

type IncidentReportReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	nowFn    func() time.Time
}

func (r *IncidentReportReconciler) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=incidentreports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=incidentreports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=incidentreports/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *IncidentReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	report := &rcav1alpha1.IncidentReport{}
	if err := r.Get(ctx, req.NamespacedName, report); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch report.Status.Phase {
	case "", phaseResolved:
		return ctrl.Result{}, nil
	case phaseDetecting:
		return r.reconcileDetecting(ctx, log, report)
	case phaseActive:
		return r.reconcileActive(ctx, log, report)
	default:
		log.Info("IncidentReport has unrecognised phase; skipping", "phase", report.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *IncidentReportReconciler) reconcileDetecting(ctx context.Context, _ logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	firstObserved := report.Status.FirstObservedAt
	if firstObserved == nil {
		firstObserved = report.Status.StartTime
	}
	if firstObserved == nil {
		return r.transitionToActive(ctx, report)
	}

	window := stabilizationWindow(report)
	elapsed := r.now().Sub(firstObserved.Time)
	if elapsed < window {
		return ctrl.Result{RequeueAfter: window - elapsed}, nil
	}

	if report.Annotations != nil {
		if value := report.Annotations[incidentAnnotationLastSeen]; value != "" {
			if parsed, err := time.Parse(time.RFC3339, value); err == nil && r.now().Sub(parsed) < window {
				return r.transitionToActive(ctx, report)
			}
		}
	}

	stillPresent, err := r.incidentStillPresent(ctx, report)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stillPresent {
		return r.transitionToActive(ctx, report)
	}

	return r.transitionToResolved(ctx, report, "Incident cleared before activation")
}

func (r *IncidentReportReconciler) reconcileActive(ctx context.Context, _ logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	lastObserved := report.Status.LastObservedAt
	if lastObserved == nil {
		lastObserved = report.Status.FirstObservedAt
	}
	if lastObserved == nil {
		lastObserved = report.Status.StartTime
	}
	if lastObserved == nil {
		lastObserved = &metav1.Time{Time: r.now()}
	}

	idle := r.now().Sub(lastObserved.Time)
	if report.Status.LastObservedAt == nil && report.Annotations != nil {
		if value := report.Annotations[incidentAnnotationLastSeen]; value != "" {
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				lastObserved = &metav1.Time{Time: parsed}
				idle = r.now().Sub(parsed)
			}
		}
	}
	if idle < healthyResolveWindow {
		return ctrl.Result{RequeueAfter: healthyResolveWindow - idle}, nil
	}

	stillPresent, err := r.incidentStillPresent(ctx, report)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stillPresent {
		return ctrl.Result{RequeueAfter: healthyResolveWindow}, nil
	}

	return r.transitionToResolved(ctx, report,
		fmt.Sprintf("No confirming signals for %.0f minutes and issue state cleared", healthyResolveWindow.Minutes()))
}

func (r *IncidentReportReconciler) transitionToActive(ctx context.Context, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	report.Status.Phase = phaseActive
	report.Status.ActiveAt = &now
	report.Status.Timeline = appendTimeline(report.Status.Timeline, now, "Incident confirmed active after stabilisation period")
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to transition IncidentReport %s/%s to Active: %w", report.Namespace, report.Name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeWarning, "IncidentActive",
			"Incident confirmed active type=%s severity=%s", report.Status.IncidentType, report.Status.Severity)
	}
	return ctrl.Result{RequeueAfter: healthyResolveWindow}, nil
}

func (r *IncidentReportReconciler) transitionToResolved(ctx context.Context, report *rcav1alpha1.IncidentReport, reason string) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	report.Status.Phase = phaseResolved
	report.Status.ResolvedTime = &now
	report.Status.ResolvedAt = &now
	report.Status.Timeline = appendTimeline(report.Status.Timeline, now, reason)
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to resolve IncidentReport %s/%s: %w", report.Namespace, report.Name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeNormal, "IncidentResolved", "%s", reason)
	}
	return ctrl.Result{}, nil
}

func (r *IncidentReportReconciler) incidentStillPresent(ctx context.Context, report *rcav1alpha1.IncidentReport) (bool, error) {
	if report.Spec.Scope.ResourceRef != nil {
		ref := report.Spec.Scope.ResourceRef
		switch ref.Kind {
		case "Node":
			return r.nodeIncidentStillPresent(ctx, ref.Name)
		case "Pod":
			return r.podIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		case "Deployment":
			return r.deploymentIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		}
	}

	if report.Spec.Scope.WorkloadRef != nil && report.Spec.Scope.WorkloadRef.Kind == "Deployment" {
		return r.deploymentIncidentStillPresent(ctx, report.Spec.Scope.WorkloadRef.Namespace, report.Spec.Scope.WorkloadRef.Name)
	}

	if report.Status.IncidentType == incidentTypeNodeFailure {
		for _, res := range report.Status.AffectedResources {
			if res.Kind == "Node" {
				return r.nodeIncidentStillPresent(ctx, res.Name)
			}
		}
	}

	for _, res := range report.Status.AffectedResources {
		if res.Kind == resourceKindPod {
			return r.podIncidentStillPresent(ctx, res.Namespace, res.Name)
		}
	}

	return false, nil
}

func (r *IncidentReportReconciler) podIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return !isPodReady(pod), nil
}

func (r *IncidentReportReconciler) nodeIncidentStillPresent(ctx context.Context, name string) (bool, error) {
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, node); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeReady:
			if cond.Status != corev1.ConditionTrue {
				return true, nil
			}
		case corev1.NodeDiskPressure, corev1.NodeMemoryPressure, corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *IncidentReportReconciler) deploymentIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	for _, cond := range deployment.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
			return true, nil
		}
	}
	return false, nil
}

func isPodReady(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func appendTimeline(tl []rcav1alpha1.TimelineEvent, t metav1.Time, msg string) []rcav1alpha1.TimelineEvent {
	tl = append(tl, rcav1alpha1.TimelineEvent{Time: t, Event: msg})
	if len(tl) > maxTimelineEntriesCtrl {
		tl = tl[len(tl)-maxTimelineEntriesCtrl:]
	}
	return tl
}

func stabilizationWindow(report *rcav1alpha1.IncidentReport) time.Duration {
	if report != nil && report.Status.StabilizationWindowSeconds > 0 {
		return time.Duration(report.Status.StabilizationWindowSeconds) * time.Second
	}
	return 30 * time.Second
}

func (r *IncidentReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.IncidentReport{}).
		Named("incidentreport").
		Complete(r)
}
