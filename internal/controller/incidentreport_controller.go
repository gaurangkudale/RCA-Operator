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
	// stabilizationDelay is how long an incident remains in Detecting before the
	// reconciler confirms it as Active. If the pod recovers within this window the
	// incident is resolved without ever becoming Active.
	stabilizationDelay = 30 * time.Second

	// healthyResolveWindow is how long an Active incident must go without receiving
	// a new watcher signal (annotationLastSeen) before the reconciler considers the
	// issue resolved. The pod must also be currently Running+Ready.
	healthyResolveWindow = 5 * time.Minute

	// incidentTypeNodeFailure is the IncidentType string used for node-level
	// incidents. These incidents store a node name in AffectedResources instead of
	// a pod name, so pod-health checks are skipped for them.
	incidentTypeNodeFailure = "NodeFailure"

	// maxTimelineEntriesCtrl caps the timeline slice on the IncidentReport status
	// to prevent unbounded growth.
	maxTimelineEntriesCtrl = 50

	// resourceKindPod is the AffectedResource Kind value used for pod-level incidents.
	resourceKindPod = "Pod"
)

// IncidentReportReconciler reconciles a IncidentReport object
type IncidentReportReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder // emits k8s Events on lifecycle transitions; nil = disabled
	nowFn    func() time.Time     // injectable for tests
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
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives the IncidentReport lifecycle: Detecting → Active → Resolved.
func (r *IncidentReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	report := &rcav1alpha1.IncidentReport{}
	if err := r.Get(ctx, req.NamespacedName, report); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch report.Status.Phase {
	case phaseResolved, "":
		// Nothing to do for resolved or empty-phase (legacy/zombie) incidents.
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

// reconcileDetecting handles Detecting-phase incidents.
// The stabilisation window (stabilizationDelay) must fully elapse before any
// decision is made.  Checking pod health mid-window is intentionally skipped:
// pods in a failure cycle (OOMKilled, CrashLoop) briefly restart as Running+Ready
// every few seconds, and an early health check would cause spurious resolution.
//
// After the window:
//   - If a new watcher signal arrived during the window (annotationLastSeen is
//     recent), the failure is still ongoing → promote to Active.
//   - If all affected pods are Running+Ready and no new signal arrived, the
//     incident was a transient blip → resolve.
//   - Otherwise → promote to Active.
func (r *IncidentReportReconciler) reconcileDetecting(ctx context.Context, log logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	if report.Status.StartTime == nil {
		// Malformed or legacy report — promote immediately to avoid being stuck.
		log.Info("IncidentReport has no StartTime; promoting to Active", "name", report.Name)
		return r.transitionToActive(ctx, report)
	}

	elapsed := r.now().Sub(report.Status.StartTime.Time)

	if elapsed < stabilizationDelay {
		// Wait out the full stabilisation window.  Do NOT check pod health here:
		// a briefly-healthy OOMKilled pod would cause immediate spurious resolution.
		remaining := stabilizationDelay - elapsed
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	// Stabilisation window elapsed.  If a new signal arrived during the window
	// (annotationLastSeen is within stabilizationDelay of now), the pod is still
	// in a failure cycle.  Promote directly to Active so the idle-window logic
	// in reconcileActive can handle the eventual resolve.
	if report.Annotations != nil {
		if lastSeenStr := report.Annotations[annotationLastSeen]; lastSeenStr != "" {
			if t, err := time.Parse(time.RFC3339, lastSeenStr); err == nil {
				if r.now().Sub(t) < stabilizationDelay {
					log.Info("Signal received during stabilisation window; promoting to Active",
						"incident", report.Name, "lastSeen", t)
					return r.transitionToActive(ctx, report)
				}
			}
		}
	}

	// No new signal during the window.  Check if pods have recovered.
	for _, res := range report.Status.AffectedResources {
		if res.Kind != resourceKindPod {
			continue
		}
		pod := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: res.Name}, pod); err != nil {
			// Pod not found or transient error — proceed to Active.
			break
		}
		if isPodReady(pod) {
			log.Info("Pod recovered after stabilisation delay; resolving",
				"pod", res.Name, "incident", report.Name)
			return r.transitionToResolved(ctx, report,
				fmt.Sprintf("Pod %s recovered before incident was confirmed active", res.Name))
		}
	}

	// Pod not healthy (or no pods) → confirm as Active.
	return r.transitionToActive(ctx, report)
}

// reconcileActive handles Active-phase incidents.
// When no new watcher signal has arrived within healthyResolveWindow AND the
// affected pod is currently Running+Ready, the incident is auto-resolved.
func (r *IncidentReportReconciler) reconcileActive(ctx context.Context, log logr.Logger, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	// Determine when the last watcher signal was received.
	lastSeenStr := ""
	if report.Annotations != nil {
		lastSeenStr = report.Annotations[annotationLastSeen]
	}

	var lastSeenTime time.Time
	if lastSeenStr != "" {
		if t, err := time.Parse(time.RFC3339, lastSeenStr); err == nil {
			lastSeenTime = t
		}
	}
	// Fall back to StartTime if annotation is absent (e.g. first reconcile after creation).
	if lastSeenTime.IsZero() && report.Status.StartTime != nil {
		lastSeenTime = report.Status.StartTime.Time
	}
	if lastSeenTime.IsZero() {
		lastSeenTime = r.now()
	}

	idle := r.now().Sub(lastSeenTime)
	if idle < healthyResolveWindow {
		return ctrl.Result{RequeueAfter: healthyResolveWindow - idle}, nil
	}

	// healthyResolveWindow has passed without a new signal. Check pod health.
	// NodeFailure incidents store a node name in AffectedResources.Name; skip
	// pod-health checks and resolve on TTL alone for those incident types.
	if report.Status.IncidentType == incidentTypeNodeFailure {
		log.Info("NodeFailure incident idle beyond resolve window; auto-resolving",
			"incident", report.Name, "idleFor", idle.Round(time.Second))
		return r.transitionToResolved(ctx, report,
			fmt.Sprintf("No new signals for %.0f minutes; incident auto-resolved", healthyResolveWindow.Minutes()))
	}

	allHealthy := true
	for _, res := range report.Status.AffectedResources {
		if res.Kind != resourceKindPod {
			continue
		}
		pod := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: res.Name}, pod); err != nil {
			// Pod not found — orphan cleanup (RCAAgentReconciler) handles the
			// resolve; do not count as "unhealthy" here to avoid double-patching.
			continue
		}
		if !isPodReady(pod) {
			allHealthy = false
			break
		}
	}

	if allHealthy {
		log.Info("All pods healthy after resolve window; auto-resolving",
			"incident", report.Name, "idleFor", idle.Round(time.Second))
		return r.transitionToResolved(ctx, report,
			fmt.Sprintf("Pod healthy for %.0f minutes; incident auto-resolved", healthyResolveWindow.Minutes()))
	}

	// Pod still unhealthy — check back after another window.
	return ctrl.Result{RequeueAfter: healthyResolveWindow}, nil
}

// transitionToActive patches the IncidentReport status to Phase=Active and
// appends a timeline entry.
func (r *IncidentReportReconciler) transitionToActive(ctx context.Context, report *rcav1alpha1.IncidentReport) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	report.Status.Phase = phaseActive
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

// transitionToResolved patches the IncidentReport status to Phase=Resolved.
func (r *IncidentReportReconciler) transitionToResolved(ctx context.Context, report *rcav1alpha1.IncidentReport, reason string) (ctrl.Result, error) {
	now := metav1.NewTime(r.now())
	base := report.DeepCopy()
	report.Status.Phase = phaseResolved
	report.Status.ResolvedTime = &now
	report.Status.Timeline = appendTimeline(report.Status.Timeline, now, reason)
	if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to resolve IncidentReport %s/%s: %w", report.Namespace, report.Name, err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeNormal, "IncidentResolved", "%s", reason)
	}
	return ctrl.Result{}, nil
}

// isPodReady returns true when pod is Running and its Ready condition is True.
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

// appendTimeline appends a TimelineEvent and trims the slice to maxTimelineEntriesCtrl.
func appendTimeline(tl []rcav1alpha1.TimelineEvent, t metav1.Time, msg string) []rcav1alpha1.TimelineEvent {
	tl = append(tl, rcav1alpha1.TimelineEvent{Time: t, Event: msg})
	if len(tl) > maxTimelineEntriesCtrl {
		tl = tl[len(tl)-maxTimelineEntriesCtrl:]
	}
	return tl
}

// SetupWithManager sets up the controller with the Manager.
func (r *IncidentReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.IncidentReport{}).
		Named("incidentreport").
		Complete(r)
}
