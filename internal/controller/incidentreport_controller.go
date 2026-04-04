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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/metrics"
	"github.com/gaurangkudale/rca-operator/internal/notify"
)

const (
	stabilizationDelay          = 5 * time.Minute
	healthyResolveWindow        = 5 * time.Minute
	incidentAnnotationLastSeen  = "rca.rca-operator.tech/last-seen"
	notificationOpenSentKey     = "rca.rca-operator.tech/notification-open-sent"
	notificationResolvedSentKey = "rca.rca-operator.tech/notification-resolved-sent"
	resolvedMetricRecordedKey   = "rca.rca-operator.tech/resolved-metric-recorded"
	annotationTrue              = "true"

	resourceKindPod = "Pod"
)

type IncidentReportReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Notifier *notify.Dispatcher
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
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcaagents,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *IncidentReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	report := &rcav1alpha1.IncidentReport{}
	if err := r.Get(ctx, req.NamespacedName, report); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch report.Status.Phase {
	case "":
		return ctrl.Result{}, nil
	case phaseDetecting:
		return r.reconcileDetecting(ctx, log, report)
	case phaseActive:
		return r.reconcileActive(ctx, log, report)
	case phaseResolved:
		return r.reconcileResolved(ctx, log, report)
	default:
		log.Info("IncidentReport has unrecognised phase; skipping", "phase", report.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *IncidentReportReconciler) reconcileDetecting(ctx context.Context, _ logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	firstObserved := incidentstatus.EffectiveStartTime(report.Status)
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
	if !report.Status.Notified {
		if err := r.sendOpenNotifications(ctx, report); err != nil {
			return ctrl.Result{}, err
		}
	}

	lastObserved := report.Status.LastObservedAt
	if lastObserved == nil {
		lastObserved = incidentstatus.EffectiveStartTime(report.Status)
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

func (r *IncidentReportReconciler) reconcileResolved(ctx context.Context, _ logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	if err := r.recordResolvedMetric(ctx, report); err != nil {
		return ctrl.Result{}, err
	}
	if report.Status.Notified {
		if err := r.sendResolvedNotifications(ctx, report); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *IncidentReportReconciler) transitionToActive(ctx context.Context, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	incidentstatus.MarkActive(report, now, "Incident confirmed active after stabilisation period")
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to transition IncidentReport %s/%s to Active: %w", report.Namespace, report.Name, err)
	}

	// Phase 1 metrics: record activation, gauge increment, and detecting→active transition duration.
	metrics.RecordIncidentActivated(report.Spec.AgentRef, report.Status.IncidentType, report.Status.Severity)
	metrics.IncActiveIncidents(report.Spec.AgentRef, report.Status.IncidentType, report.Status.Severity)
	if start := incidentstatus.EffectiveStartTime(base.Status); start != nil {
		metrics.ObserveIncidentTransition("detecting", "active", now.Sub(start.Time).Seconds())
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(report, nil, corev1.EventTypeWarning, "IncidentActive", "Activate",
			"Incident confirmed active type=%s severity=%s", report.Status.IncidentType, report.Status.Severity)
	}
	return ctrl.Result{RequeueAfter: healthyResolveWindow}, nil
}

func (r *IncidentReportReconciler) transitionToResolved(ctx context.Context, report *rcav1alpha1.IncidentReport, reason string) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	incidentstatus.MarkResolved(report, now, reason)
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to resolve IncidentReport %s/%s: %w", report.Namespace, report.Name, err)
	}

	// Phase 1 metrics: record the phase transition duration and update the active gauge.
	switch base.Status.Phase {
	case phaseDetecting:
		if start := incidentstatus.EffectiveStartTime(base.Status); start != nil {
			metrics.ObserveIncidentTransition("detecting", "resolved", now.Sub(start.Time).Seconds())
		}
	case phaseActive:
		metrics.DecActiveIncidents(report.Spec.AgentRef, report.Status.IncidentType, report.Status.Severity)
		if base.Status.ActiveAt != nil {
			metrics.ObserveIncidentTransition("active", "resolved", now.Sub(base.Status.ActiveAt.Time).Seconds())
		}
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(report, nil, corev1.EventTypeNormal, "IncidentResolved", "Resolve",
			"%s", reason)
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
		case "StatefulSet":
			return r.statefulSetIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		case "DaemonSet":
			return r.daemonSetIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		case "Job":
			return r.jobIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		case "CronJob":
			return r.cronJobIncidentStillPresent(ctx, ref.Namespace, ref.Name)
		}
	}

	if report.Spec.Scope.WorkloadRef != nil {
		wref := report.Spec.Scope.WorkloadRef
		switch wref.Kind {
		case "Deployment":
			return r.deploymentIncidentStillPresent(ctx, wref.Namespace, wref.Name)
		case "StatefulSet":
			return r.statefulSetIncidentStillPresent(ctx, wref.Namespace, wref.Name)
		case "DaemonSet":
			return r.daemonSetIncidentStillPresent(ctx, wref.Namespace, wref.Name)
		case "Job":
			return r.jobIncidentStillPresent(ctx, wref.Namespace, wref.Name)
		case "CronJob":
			return r.cronJobIncidentStillPresent(ctx, wref.Namespace, wref.Name)
		}
	}

	// Check affected resources: nodes first, then pods.
	for _, res := range report.Status.AffectedResources {
		if res.Kind == "Node" {
			return r.nodeIncidentStillPresent(ctx, res.Name)
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

func (r *IncidentReportReconciler) statefulSetIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sts); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	// Stall: UpdateRevision != CurrentRevision and not all pods updated.
	if sts.Status.UpdateRevision != sts.Status.CurrentRevision {
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if sts.Status.UpdatedReplicas < desired {
			return true, nil
		}
	}
	return false, nil
}

func (r *IncidentReportReconciler) daemonSetIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	ds := &appsv1.DaemonSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ds); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	// Stall: updated < desired.
	if ds.Status.DesiredNumberScheduled > 0 && ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
		return true, nil
	}
	return false, nil
}

func (r *IncidentReportReconciler) jobIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, job); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return false, nil
}

func (r *IncidentReportReconciler) cronJobIncidentStillPresent(ctx context.Context, namespace, name string) (bool, error) {
	// CronJob incidents resolve when the CronJob no longer exists or when
	// its most recent active/completed job is not in a Failed state.
	// Since CronJob failures are transient (next run may succeed), we consider
	// the incident still present only if the CronJob object still exists.
	// The healthyResolveWindow handles the time-based resolution.
	cj := &batchv1.CronJob{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cj); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	// CronJob exists — let the time-based window handle resolution.
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

func stabilizationWindow(report *rcav1alpha1.IncidentReport) time.Duration {
	if report != nil && report.Status.StabilizationWindowSeconds > 0 {
		return time.Duration(report.Status.StabilizationWindowSeconds) * time.Second
	}
	return 30 * time.Second
}

func (r *IncidentReportReconciler) sendOpenNotifications(ctx context.Context, report *rcav1alpha1.IncidentReport) error {
	if r.Notifier == nil {
		return nil
	}
	if err := r.Notifier.NotifyIncident(ctx, report, "trigger"); err != nil {
		return fmt.Errorf("notify open incident %s/%s: %w", report.Namespace, report.Name, err)
	}

	base := report.DeepCopy()
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}
	report.Annotations[notificationOpenSentKey] = annotationTrue
	report.Status.Notified = true
	if err := r.Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("mark open notification metadata for %s/%s: %w", report.Namespace, report.Name, err)
	}
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("mark incident notified for %s/%s: %w", report.Namespace, report.Name, err)
	}
	return nil
}

func (r *IncidentReportReconciler) sendResolvedNotifications(ctx context.Context, report *rcav1alpha1.IncidentReport) error {
	if report.Annotations != nil && report.Annotations[notificationResolvedSentKey] == annotationTrue {
		return nil
	}
	if r.Notifier == nil {
		return nil
	}
	if err := r.Notifier.NotifyIncident(ctx, report, "resolve"); err != nil {
		return fmt.Errorf("notify resolved incident %s/%s: %w", report.Namespace, report.Name, err)
	}

	base := report.DeepCopy()
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}
	report.Annotations[notificationResolvedSentKey] = annotationTrue
	if err := r.Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("mark resolved notification metadata for %s/%s: %w", report.Namespace, report.Name, err)
	}
	return nil
}

func (r *IncidentReportReconciler) recordResolvedMetric(ctx context.Context, report *rcav1alpha1.IncidentReport) error {
	if report.Annotations != nil && report.Annotations[resolvedMetricRecordedKey] == annotationTrue {
		return nil
	}

	metrics.RecordIncidentResolved(report.Spec.AgentRef, report.Status.IncidentType, report.Status.Severity)

	base := report.DeepCopy()
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}
	report.Annotations[resolvedMetricRecordedKey] = annotationTrue
	if err := r.Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("mark resolved metric metadata for %s/%s: %w", report.Namespace, report.Name, err)
	}
	return nil
}

func (r *IncidentReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.IncidentReport{}).
		Named("incidentreport").
		Complete(r)
}
