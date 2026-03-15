// Package reporter handles IncidentReport CR creation, patching, and resolution.
// It is the single source of truth for all Kubernetes API writes that manage
// the IncidentReport lifecycle; the correlator consumer delegates all CR
// operations here, keeping the consumer focused on event routing.
package reporter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// Annotation and label keys written on every IncidentReport.
const (
	AnnotationDedupKey   = "rca.rca-operator.io/dedup-key"
	AnnotationSignal     = "rca.rca-operator.io/signal"
	AnnotationLastSeen   = "rca.rca-operator.io/last-seen"
	AnnotationSignalSeen = "rca.rca-operator.io/signal-count"

	LabelAgent        = "rca.rca-operator.io/agent"
	LabelSeverity     = "rca.rca-operator.io/severity"
	LabelIncidentType = "rca.rca-operator.io/incident-type"
	LabelPodName      = "rca.rca-operator.io/pod"
)

// Phase constants for the IncidentReport lifecycle.
const (
	PhaseDetecting = "Detecting"
	PhaseActive    = "Active"
	PhaseResolved  = "Resolved"
	ValueUnknown   = "unknown"
)

// Well-known incident type identifiers.
const (
	IncidentTypeNodeFailure = "NodeFailure"
	IncidentTypeRegistry    = "Registry"
)

// Timing constants that govern signal cooldown and reopen eligibility.
const (
	// SignalCooldown is the minimum idle time after the last watcher signal
	// before an incident may be resolved by a PodHealthy event. It prevents
	// brief restart cycles (OOMKilled, CrashLoop) from prematurely resolving an
	// incident, which would cause a duplicate to be created on the next failure.
	SignalCooldown = 5 * time.Minute

	// ReopenWindow is the maximum age of a Resolved incident that will be
	// transitioned back to Detecting when a new signal arrives for the same pod
	// and incident type. Older resolved incidents are left alone and a fresh
	// IncidentReport is created instead.
	ReopenWindow = 30 * time.Minute
)

// Entry-count caps for in-memory status sub-resources.
const (
	MaxTimelineEntries = 50
	MaxSignalEntries   = 20
)

// Reporter handles IncidentReport CR creation, patching, and resolution.
// All write paths that modify IncidentReport objects flow through this type.
// It is safe to create a single Reporter per Consumer and share across goroutines
// PROVIDED the caller serialises calls (as the Consumer event loop does).
type Reporter struct {
	client   client.Client
	log      logr.Logger
	Recorder record.EventRecorder // optional; nil skips Kubernetes event emission
	Now      func() time.Time     // injectable clock; defaults to time.Now

	// openRegistryByNS is a best-effort in-memory cache: namespace → name of
	// the canonical open Registry IncidentReport. It is populated on creation
	// and reopen, and consulted before every API list to bypass informer-cache
	// latency during rapid bootstrap-scan event bursts.
	openRegistryByNS map[string]string
}

// NewReporter returns a Reporter backed by the given client.
func NewReporter(c client.Client, logger logr.Logger) *Reporter {
	return &Reporter{
		client:           c,
		log:              logger.WithName("cr-reporter"),
		Now:              time.Now,
		openRegistryByNS: make(map[string]string),
	}
}

// EnsureIncident is the main CR-write entry point for non-healthy events.
// It implements the four-step deduplication flow:
//  1. Find an existing open (Detecting or Active) incident → update it.
//  2. Find a recently-resolved incident within ReopenWindow → reopen it.
//  3. Create a fresh IncidentReport.
func (r *Reporter) EnsureIncident(
	ctx context.Context,
	namespace, podName, agentRef, incidentType, severity, summary, dedupKey string,
	occurredAt time.Time,
) error {
	active, err := r.findOpenIncident(ctx, namespace, podName, incidentType)
	if err != nil {
		return err
	}
	if active != nil {
		return r.updateActiveIncident(ctx, active, dedupKey, podName, severity, summary)
	}

	resolved, err := r.findResolvableIncident(ctx, namespace, podName, incidentType)
	if err != nil {
		return err
	}
	if resolved != nil {
		return r.reopenIncident(ctx, resolved, dedupKey, podName, severity, summary)
	}

	if agentRef == "" {
		agentRef = "unknown-agent"
	}
	if occurredAt.IsZero() {
		occurredAt = r.Now()
	}
	startTime := metav1.NewTime(occurredAt)

	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s-", strings.ToLower(incidentType), safeNameToken(podName)),
			Namespace:    namespace,
			Labels: map[string]string{
				LabelAgent:        agentRef,
				LabelSeverity:     severity,
				LabelIncidentType: incidentType,
				LabelPodName:      safeLabelValue(podName),
			},
			Annotations: map[string]string{
				AnnotationSignal:     summary,
				AnnotationDedupKey:   dedupKey,
				AnnotationLastSeen:   startTime.Format(time.RFC3339),
				AnnotationSignalSeen: "1",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: agentRef},
	}

	if err := r.client.Create(ctx, report); err != nil {
		return fmt.Errorf("failed to create IncidentReport: %w", err)
	}
	// Populate the Registry dedup cache immediately after creation so that
	// back-to-back bootstrap-scan events for other pods in the same namespace
	// find this incident without waiting for the informer cache to catch up.
	if incidentType == IncidentTypeRegistry {
		r.openRegistryByNS[namespace] = report.Name
	}

	statusBase := report.DeepCopy()
	report.Status = rcav1alpha1.IncidentReportStatus{
		Severity:     severity,
		Phase:        PhaseDetecting,
		IncidentType: incidentType,
		StartTime:    &startTime,
		ResolvedTime: nil,
		Notified:     false,
		AffectedResources: []rcav1alpha1.AffectedResource{
			{Kind: "Pod", Name: podName, Namespace: namespace},
		},
		CorrelatedSignals: []string{summary},
		Timeline:          []rcav1alpha1.TimelineEvent{{Time: startTime, Event: summary}},
		RootCause:         "",
	}
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	r.log.Info("Created IncidentReport",
		"namespace", namespace,
		"name", report.Name,
		"incidentType", incidentType,
		"severity", severity,
	)
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeWarning, "IncidentDetected",
			"New %s incident detected severity=%s: %s", incidentType, severity, summary)
	}
	return nil
}

// ResolveForHealthyPod resolves all open incidents that reference podName,
// provided the last watcher signal is older than SignalCooldown.
// It first confirms the pod is currently Running+Ready; stale healthy signals
// are silently ignored.
func (r *Reporter) ResolveForHealthyPod(ctx context.Context, namespace, podName string) error {
	currentPod := &corev1.Pod{}
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, currentPod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to fetch pod for resolve check: %w", err)
	}
	if !isPodCurrentlyReady(currentPod) {
		return nil
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list IncidentReports for resolve: %w", err)
	}

	now := metav1.NewTime(r.Now())
	resolvedCount := 0
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved {
			continue
		}
		if !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		// Guard: skip incidents that received a watcher signal within SignalCooldown.
		// Pods that briefly restart between failure cycles (OOMKilled, CrashLoop)
		// become Running+Ready for a few seconds, which would otherwise prematurely
		// resolve the incident and cause a new one to be created on the next cycle.
		// The reconciler's idle-window logic handles final resolution for these cases.
		if lastSeen := report.Annotations[AnnotationLastSeen]; lastSeen != "" {
			if t, err := time.Parse(time.RFC3339, lastSeen); err == nil {
				if r.Now().Sub(t) < SignalCooldown {
					continue
				}
			}
		}

		base := report.DeepCopy()
		report.Status.Phase = PhaseResolved
		report.Status.ResolvedTime = &now
		report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
			Time:  now,
			Event: fmt.Sprintf("Pod %s became Running and Ready", podName),
		})
		report.Status.Timeline = trimTimeline(report.Status.Timeline)
		if err := r.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status: %w", err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(report, corev1.EventTypeNormal, "IncidentResolved",
				"Pod %s became Running and Ready; incident resolved", podName)
		}
		resolvedCount++
	}

	if resolvedCount > 0 {
		r.log.Info("Resolved IncidentReports from pod healthy signal",
			"namespace", namespace,
			"pod", podName,
			"count", resolvedCount,
		)
	}
	return nil
}

// ResolveForDeletedPod marks all open incidents referencing podName as Resolved.
func (r *Reporter) ResolveForDeletedPod(ctx context.Context, namespace, podName string) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list IncidentReports for deleted-pod resolve: %w", err)
	}

	now := metav1.NewTime(r.Now())
	resolvedCount := 0
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved {
			continue
		}
		if !incidentAffectsPod(report, podName, namespace) {
			continue
		}

		base := report.DeepCopy()
		report.Status.Phase = PhaseResolved
		report.Status.ResolvedTime = &now
		report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
			Time:  now,
			Event: fmt.Sprintf("Pod %s was deleted from the cluster", podName),
		})
		report.Status.Timeline = trimTimeline(report.Status.Timeline)
		if err := r.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status for deleted pod: %w", err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(report, corev1.EventTypeNormal, "IncidentResolved",
				"Pod %s was deleted from the cluster; incident resolved", podName)
		}
		resolvedCount++
	}

	if resolvedCount > 0 {
		r.log.Info("Resolved IncidentReports for deleted pod",
			"namespace", namespace,
			"pod", podName,
			"count", resolvedCount,
		)
	}
	return nil
}

// Consolidate is called once at startup. It finds all open Registry
// IncidentReports per namespace, keeps the oldest as the canonical incident,
// merges AffectedResources from duplicates into it, and marks duplicates as
// Resolved. This cleans up incidents that were created in a previous run's
// bootstrap-scan race where multiple pods signalled ImagePullBackOff
// simultaneously before the informer cache reflected the first created incident.
func (r *Reporter) Consolidate(ctx context.Context) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list,
		client.MatchingLabels{LabelIncidentType: IncidentTypeRegistry},
	); err != nil {
		return fmt.Errorf("consolidate registry: list failed: %w", err)
	}

	type nsGroup struct {
		canonical *rcav1alpha1.IncidentReport
		extras    []*rcav1alpha1.IncidentReport
	}
	groups := make(map[string]*nsGroup)
	for i := range list.Items {
		item := &list.Items[i]
		if item.Status.Phase == PhaseResolved {
			continue
		}
		g, ok := groups[item.Namespace]
		if !ok {
			groups[item.Namespace] = &nsGroup{canonical: item}
			continue
		}
		if item.CreationTimestamp.Before(&g.canonical.CreationTimestamp) {
			g.extras = append(g.extras, g.canonical)
			g.canonical = item
		} else {
			g.extras = append(g.extras, item)
		}
	}

	now := metav1.NewTime(r.Now())
	for ns, g := range groups {
		if len(g.extras) == 0 {
			r.openRegistryByNS[ns] = g.canonical.Name
			continue
		}

		// Merge AffectedResources from duplicates into canonical.
		canonicalBase := g.canonical.DeepCopy()
		for _, extra := range g.extras {
			for _, res := range extra.Status.AffectedResources {
				if !incidentAffectsPod(g.canonical, res.Name, res.Namespace) {
					g.canonical.Status.AffectedResources = append(g.canonical.Status.AffectedResources, res)
				}
			}
		}
		if err := r.client.Status().Patch(ctx, g.canonical, client.MergeFrom(canonicalBase)); err != nil {
			r.log.Error(err, "consolidate registry: failed to update canonical incident",
				"namespace", ns, "name", g.canonical.Name)
		}

		// Resolve duplicate incidents.
		for _, extra := range g.extras {
			base := extra.DeepCopy()
			extra.Status.Phase = PhaseResolved
			extra.Status.ResolvedTime = &now
			extra.Status.Timeline = append(extra.Status.Timeline, rcav1alpha1.TimelineEvent{
				Time:  now,
				Event: fmt.Sprintf("Merged into canonical incident %s during startup consolidation", g.canonical.Name),
			})
			extra.Status.Timeline = trimTimeline(extra.Status.Timeline)
			if err := r.client.Status().Patch(ctx, extra, client.MergeFrom(base)); err != nil {
				r.log.Error(err, "consolidate registry: failed to resolve duplicate incident",
					"namespace", ns, "name", extra.Name)
			} else {
				r.log.Info("Resolved duplicate Registry incident during startup consolidation",
					"namespace", ns,
					"resolved", extra.Name,
					"canonical", g.canonical.Name,
				)
			}
		}
		r.openRegistryByNS[ns] = g.canonical.Name
	}
	return nil
}

// findOpenIncident returns the first non-Resolved IncidentReport (Detecting or
// Active) for the given pod and incident type, or nil if none exists.
//
// Registry incidents are namespace-scoped: all pods that fail to pull an image
// share one report per namespace, so the pod-name check is skipped for that
// type. An in-memory cache is checked first to avoid API informer-cache latency
// during rapid bootstrap-scan event bursts.
func (r *Reporter) findOpenIncident(ctx context.Context, namespace, podName, incidentType string) (*rcav1alpha1.IncidentReport, error) {
	if incidentType == IncidentTypeRegistry {
		if name, ok := r.openRegistryByNS[namespace]; ok {
			report := &rcav1alpha1.IncidentReport{}
			if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, report); err == nil {
				if report.Status.Phase != PhaseResolved {
					return report.DeepCopy(), nil
				}
			}
			// Cache is stale (incident resolved or deleted); fall through to list.
			delete(r.openRegistryByNS, namespace)
		}
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports: %w", err)
	}
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved {
			continue
		}
		if report.Status.IncidentType != incidentType {
			continue
		}
		if incidentType == IncidentTypeRegistry {
			r.openRegistryByNS[namespace] = report.Name // refresh cache
			return report.DeepCopy(), nil
		}
		if !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		return report.DeepCopy(), nil
	}
	return nil, nil
}

// findResolvableIncident returns the most recently resolved IncidentReport for
// the given pod and incident type, provided it was resolved within ReopenWindow.
// Registry incidents are namespace-scoped: pod name is ignored.
func (r *Reporter) findResolvableIncident(ctx context.Context, namespace, podName, incidentType string) (*rcav1alpha1.IncidentReport, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports for reopen check: %w", err)
	}

	var best *rcav1alpha1.IncidentReport
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase != PhaseResolved {
			continue
		}
		if report.Status.IncidentType != incidentType {
			continue
		}
		if report.Status.ResolvedTime == nil {
			continue
		}
		if r.Now().Sub(report.Status.ResolvedTime.Time) > ReopenWindow {
			continue
		}
		if incidentType != IncidentTypeRegistry {
			if !incidentAffectsPod(report, podName, namespace) {
				continue
			}
		}
		if best == nil || report.Status.ResolvedTime.After(best.Status.ResolvedTime.Time) {
			best = report.DeepCopy()
		}
	}
	return best, nil
}

func (r *Reporter) updateActiveIncident(
	ctx context.Context,
	report *rcav1alpha1.IncidentReport,
	dedupKey, podName, severity, summary string,
) error {
	now := r.Now()
	nowTime := metav1.NewTime(now)

	if report.Labels == nil {
		report.Labels = make(map[string]string)
	}
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}

	metaBase := report.DeepCopy()
	report.Labels[LabelSeverity] = higherSeverity(report.Labels[LabelSeverity], severity)
	report.Annotations[AnnotationSignal] = summary
	report.Annotations[AnnotationDedupKey] = dedupKey
	report.Annotations[AnnotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[AnnotationSignalSeen] = incrementCounter(report.Annotations[AnnotationSignalSeen])
	if err := r.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Severity = higherSeverity(report.Status.Severity, severity)
	report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{Time: nowTime, Event: summary})
	report.Status.Timeline = trimTimeline(report.Status.Timeline)
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	// For namespace-scoped incident types (e.g. Registry), each new pod that
	// contributes a signal is added to AffectedResources so the reconciler can
	// track all of them when deciding whether to auto-resolve.
	if podName != "" && !incidentAffectsPod(report, podName, report.Namespace) {
		report.Status.AffectedResources = append(report.Status.AffectedResources, rcav1alpha1.AffectedResource{
			Kind:      "Pod",
			Name:      podName,
			Namespace: report.Namespace,
		})
	}
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	r.log.Info("Updated active IncidentReport from repeated watcher signal",
		"namespace", report.Namespace,
		"name", report.Name,
	)
	return nil
}

func (r *Reporter) reopenIncident(
	ctx context.Context,
	report *rcav1alpha1.IncidentReport,
	dedupKey, podName, severity, summary string,
) error {
	now := r.Now()
	nowTime := metav1.NewTime(now)

	if report.Labels == nil {
		report.Labels = make(map[string]string)
	}
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}

	metaBase := report.DeepCopy()
	report.Labels[LabelSeverity] = higherSeverity(report.Labels[LabelSeverity], severity)
	report.Annotations[AnnotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[AnnotationSignalSeen] = incrementCounter(report.Annotations[AnnotationSignalSeen])
	report.Annotations[AnnotationSignal] = summary
	report.Annotations[AnnotationDedupKey] = dedupKey
	if err := r.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata on reopen: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Phase = PhaseDetecting
	report.Status.ResolvedTime = nil
	report.Status.StartTime = &nowTime
	report.Status.Severity = higherSeverity(report.Status.Severity, severity)
	report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
		Time:  nowTime,
		Event: fmt.Sprintf("Incident re-opened: %s", summary),
	})
	report.Status.Timeline = trimTimeline(report.Status.Timeline)
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	if podName != "" && !incidentAffectsPod(report, podName, report.Namespace) {
		report.Status.AffectedResources = append(report.Status.AffectedResources, rcav1alpha1.AffectedResource{
			Kind:      "Pod",
			Name:      podName,
			Namespace: report.Namespace,
		})
	}
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status on reopen: %w", err)
	}

	r.log.Info("Reopened resolved IncidentReport",
		"namespace", report.Namespace,
		"name", report.Name,
		"incidentType", report.Status.IncidentType,
	)
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeWarning, "IncidentReopened",
			"Incident re-opened: %s", summary)
	}
	// Refresh Registry dedup cache so subsequent events route to this incident.
	if report.Status.IncidentType == IncidentTypeRegistry {
		r.openRegistryByNS[report.Namespace] = report.Name
	}
	return nil
}

func incidentAffectsPod(report *rcav1alpha1.IncidentReport, podName, namespace string) bool {
	for _, resource := range report.Status.AffectedResources {
		if resource.Kind != "Pod" {
			continue
		}
		if resource.Name == podName && resource.Namespace == namespace {
			return true
		}
	}
	return false
}

func isPodCurrentlyReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func trimTimeline(in []rcav1alpha1.TimelineEvent) []rcav1alpha1.TimelineEvent {
	if len(in) <= MaxTimelineEntries {
		return in
	}
	return in[len(in)-MaxTimelineEntries:]
}

func trimSignals(in []string) []string {
	if len(in) <= MaxSignalEntries {
		return in
	}
	return in[len(in)-MaxSignalEntries:]
}

func incrementCounter(current string) string {
	if current == "" {
		return "1"
	}
	n, err := strconv.Atoi(current)
	if err != nil || n < 0 {
		return "1"
	}
	return strconv.Itoa(n + 1)
}

func higherSeverity(current, incoming string) string {
	rank := map[string]int{"P1": 4, "P2": 3, "P3": 2, "P4": 1}
	if rank[incoming] > rank[current] {
		return incoming
	}
	if current == "" {
		return incoming
	}
	return current
}

func safeLabelValue(in string) string {
	if in == "" {
		return ValueUnknown
	}
	replaced := strings.ToLower(in)
	b := strings.Builder{}
	b.Grow(len(replaced))
	for _, r := range replaced {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '.' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return ValueUnknown
	}
	if len(out) > 63 {
		return out[:63]
	}
	return out
}

func safeNameToken(in string) string {
	if in == "" {
		return "incident"
	}
	replaced := strings.ToLower(in)
	b := strings.Builder{}
	b.Grow(len(replaced))
	for _, r := range replaced {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "incident"
	}
	return out
}
