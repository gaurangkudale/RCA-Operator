// Package reporter handles IncidentReport CR creation, patching, and resolution.
// It is the single source of truth for all Kubernetes API writes that manage
// the IncidentReport lifecycle.
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
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/metrics"
)

const (
	AnnotationDedupKey   = "rca.rca-operator.tech/dedup-key"
	AnnotationSignal     = "rca.rca-operator.tech/signal"
	AnnotationLastSeen   = "rca.rca-operator.tech/last-seen"
	AnnotationSignalSeen = "rca.rca-operator.tech/signal-count"

	LabelAgent           = "rca.rca-operator.tech/agent"
	LabelSeverity        = "rca.rca-operator.tech/severity"
	LabelIncidentType    = "rca.rca-operator.tech/incident-type"
	LabelPodName         = "rca.rca-operator.tech/pod"
	LabelFingerprintHash = "rca.rca-operator.tech/fingerprint-hash"
)

const (
	PhaseDetecting   = "Detecting"
	PhaseActive      = "Active"
	PhaseResolved    = "Resolved"
	ValueUnknown     = "unknown"
	resourceKindPod  = "Pod"
	defaultNameToken = "incident"
)

const (
	SignalCooldown = 5 * time.Minute
	ReopenWindow   = 30 * time.Minute
)

const (
	MaxTimelineEntries = incidentstatus.MaxTimelineEntries
	MaxSignalEntries   = 20
)

type Reporter struct {
	client   client.Client
	log      logr.Logger
	Recorder events.EventRecorder
	Now      func() time.Time

	openByFingerprint map[string]types.NamespacedName
}

func NewReporter(c client.Client, logger logr.Logger) *Reporter {
	return &Reporter{
		client:            c,
		log:               logger.WithName("incident-engine"),
		Now:               time.Now,
		openByFingerprint: make(map[string]types.NamespacedName),
	}
}

func (r *Reporter) EnsureIncident(
	ctx context.Context,
	namespace, podName, agentRef, incidentType, severity, summary, dedupKey string,
	occurredAt time.Time,
) error {
	input := incident.Input{
		Namespace:    namespace,
		AgentRef:     agentRef,
		IncidentType: incidentType,
		Severity:     severity,
		Summary:      summary,
		Message:      summary,
		DedupKey:     dedupKey,
		ObservedAt:   occurredAt,
		Scope: rcav1alpha1.IncidentScope{
			Level:     incident.ScopeLevelPod,
			Namespace: namespace,
			ResourceRef: &rcav1alpha1.IncidentObjectRef{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       resourceKindPod,
				Namespace:  namespace,
				Name:       podName,
			},
		},
		AffectedResources: []rcav1alpha1.AffectedResource{
			{APIVersion: corev1.SchemeGroupVersion.String(), Kind: resourceKindPod, Namespace: namespace, Name: podName},
		},
	}
	return r.EnsureSignal(ctx, input)
}

func (r *Reporter) EnsureSignal(ctx context.Context, input incident.Input) error {
	if input.AgentRef == "" {
		input.AgentRef = "unknown-agent"
	}
	if input.ObservedAt.IsZero() {
		input.ObservedAt = r.Now()
	}
	if input.Summary == "" {
		input.Summary = incident.SummaryFromParts(input.IncidentType, input.Reason, input.Message)
	}
	if input.Message == "" {
		input.Message = input.Summary
	}

	fingerprint := input.Fingerprint()
	fingerprintHash := incident.FingerprintHash(fingerprint)

	active, err := r.findOpenIncident(ctx, input.Namespace, fingerprint, fingerprintHash)
	if err != nil {
		return err
	}
	if active != nil {
		return r.updateActiveIncident(ctx, active, input, fingerprint, fingerprintHash)
	}

	resolved, err := r.findResolvableIncident(ctx, input.Namespace, fingerprint, fingerprintHash)
	if err != nil {
		return err
	}
	if resolved != nil {
		return r.reopenIncident(ctx, resolved, input, fingerprint, fingerprintHash)
	}

	// Workload-ref fallback: catches duplicates when the fingerprint is
	// inconsistent across operator restarts or transient enrichment failures
	// (e.g., ReplicaSet owner lookup fails → RS-scoped fingerprint instead of
	// Deployment-scoped). Without this guard a second incident would be
	// created for the same workload while the first still exists (open or
	// recently resolved), producing the "one resolved, one active" duplicate
	// visible in the UI.
	if input.Scope.WorkloadRef != nil {
		existing, err := r.findExistingByWorkloadRef(ctx, input)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Status.Phase == PhaseResolved {
				return r.reopenIncident(ctx, existing, input, fingerprint, fingerprintHash)
			}
			return r.updateActiveIncident(ctx, existing, input, fingerprint, fingerprintHash)
		}
	}

	return r.createIncident(ctx, input, fingerprint, fingerprintHash)
}

func (r *Reporter) createIncident(ctx context.Context, input incident.Input, fingerprint, fingerprintHash string) error {
	firstSeen := metav1.NewTime(input.ObservedAt)
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s-", strings.ToLower(input.IncidentType), safeNameToken(primaryResourceName(input))),
			Namespace:    input.Namespace,
			Labels: map[string]string{
				LabelAgent:           input.AgentRef,
				LabelSeverity:        input.Severity,
				LabelIncidentType:    input.IncidentType,
				LabelPodName:         safeLabelValue(primaryPodName(input)),
				LabelFingerprintHash: fingerprintHash,
			},
			Annotations: map[string]string{
				AnnotationSignal:     input.Summary,
				AnnotationDedupKey:   input.DedupKey,
				AnnotationLastSeen:   firstSeen.Format(time.RFC3339),
				AnnotationSignalSeen: "1",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     input.AgentRef,
			Fingerprint:  fingerprint,
			IncidentType: input.IncidentType,
			Scope:        input.Scope,
		},
	}
	if err := r.client.Create(ctx, report); err != nil {
		return fmt.Errorf("failed to create IncidentReport: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status = rcav1alpha1.IncidentReportStatus{
		Severity:                   input.Severity,
		Phase:                      PhaseDetecting,
		IncidentType:               input.IncidentType,
		Summary:                    input.Summary,
		Reason:                     input.Reason,
		Message:                    input.Message,
		FirstObservedAt:            &firstSeen,
		LastObservedAt:             &firstSeen,
		StartTime:                  &firstSeen,
		ResolvedTime:               nil,
		ResolvedAt:                 nil,
		ActiveAt:                   nil,
		SignalCount:                1,
		StabilizationWindowSeconds: int64((5 * time.Minute).Seconds()),
		Notified:                   false,
		AffectedResources:          trimAffectedResources(input.AffectedResources),
		CorrelatedSignals:          []string{input.Summary},
		Timeline:                   incidentstatus.AppendTimeline(nil, firstSeen, input.Summary),
	}
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
	metrics.RecordIncidentDetected(input.AgentRef, input.IncidentType, input.Severity)
	r.log.Info("Created IncidentReport",
		"namespace", report.Namespace,
		"name", report.Name,
		"incidentType", input.IncidentType,
		"fingerprint", fingerprint,
	)
	if r.Recorder != nil {
		r.Recorder.Eventf(report, nil, corev1.EventTypeWarning, "IncidentDetected", "Detect",
			"New %s incident detected severity=%s: %s", input.IncidentType, input.Severity, input.Summary)
	}
	return nil
}

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
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved || !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		lastObserved := report.Status.LastObservedAt
		if lastObserved == nil && report.Annotations != nil {
			if lastSeen := report.Annotations[AnnotationLastSeen]; lastSeen != "" {
				if parsed, err := time.Parse(time.RFC3339, lastSeen); err == nil {
					lastObserved = &metav1.Time{Time: parsed}
				}
			}
		}
		if lastObserved != nil && r.Now().Sub(lastObserved.Time) < SignalCooldown {
			continue
		}
		base := report.DeepCopy()
		incidentstatus.MarkResolved(report, now, fmt.Sprintf("Pod %s became Running and Ready", podName))
		if err := r.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status: %w", err)
		}
	}
	return nil
}

func (r *Reporter) ResolveForDeletedPod(ctx context.Context, namespace, podName string) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list IncidentReports for deleted-pod resolve: %w", err)
	}

	now := metav1.NewTime(r.Now())
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved || !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		base := report.DeepCopy()
		incidentstatus.MarkResolved(report, now, fmt.Sprintf("Pod %s was deleted from the cluster", podName))
		if err := r.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status for deleted pod: %w", err)
		}
	}
	return nil
}

func (r *Reporter) Consolidate(ctx context.Context) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list); err != nil {
		return fmt.Errorf("consolidate incidents: list failed: %w", err)
	}

	type group struct {
		canonical *rcav1alpha1.IncidentReport
		extras    []*rcav1alpha1.IncidentReport
	}
	groups := make(map[string]*group)
	for i := range list.Items {
		item := &list.Items[i]
		if item.Status.Phase == PhaseResolved {
			continue
		}
		fingerprint := reportFingerprint(item)
		if fingerprint == "" {
			continue
		}
		key := item.Namespace + "/" + fingerprint
		if existing, ok := groups[key]; ok {
			if item.CreationTimestamp.Before(&existing.canonical.CreationTimestamp) {
				existing.extras = append(existing.extras, existing.canonical)
				existing.canonical = item
			} else {
				existing.extras = append(existing.extras, item)
			}
			continue
		}
		groups[key] = &group{canonical: item}
	}

	now := metav1.NewTime(r.Now())
	for fingerprint, group := range groups {
		fp := reportFingerprint(group.canonical)
		r.openByFingerprint[fp] = types.NamespacedName{
			Namespace: group.canonical.Namespace,
			Name:      group.canonical.Name,
		}

		// Back-fill Spec.Fingerprint on legacy incidents that were created
		// before the field was introduced. Without this, future fingerprint-
		// based lookups fall back to the computed value which may differ from
		// what a new enriched signal produces, resulting in duplicate incidents.
		if group.canonical.Spec.Fingerprint == "" {
			metaBase := group.canonical.DeepCopy()
			group.canonical.Spec.Fingerprint = fp
			if err := r.client.Patch(ctx, group.canonical, client.MergeFrom(metaBase)); err != nil {
				r.log.Error(err, "failed to backfill fingerprint on canonical incident during consolidation",
					"namespace", group.canonical.Namespace, "name", group.canonical.Name)
			}
		}

		if len(group.extras) == 0 {
			continue
		}

		canonicalBase := group.canonical.DeepCopy()
		for _, extra := range group.extras {
			group.canonical.Status.AffectedResources = mergeAffectedResources(group.canonical.Status.AffectedResources, extra.Status.AffectedResources)
		}
		if err := r.client.Status().Patch(ctx, group.canonical, client.MergeFrom(canonicalBase)); err != nil {
			r.log.Error(err, "failed to update canonical incident during consolidation", "fingerprint", fingerprint)
		}

		for _, extra := range group.extras {
			base := extra.DeepCopy()
			incidentstatus.MarkResolved(
				extra,
				now,
				fmt.Sprintf("Merged into canonical incident %s during startup consolidation", group.canonical.Name),
			)
			if err := r.client.Status().Patch(ctx, extra, client.MergeFrom(base)); err != nil {
				r.log.Error(err, "failed to resolve duplicate incident during consolidation",
					"namespace", extra.Namespace, "name", extra.Name)
			}
		}
	}
	return nil
}

func (r *Reporter) findOpenIncident(ctx context.Context, namespace, fingerprint, hash string) (*rcav1alpha1.IncidentReport, error) {
	if ref, ok := r.openByFingerprint[fingerprint]; ok {
		report := &rcav1alpha1.IncidentReport{}
		if err := r.client.Get(ctx, ref, report); err == nil && report.Status.Phase != PhaseResolved && reportFingerprint(report) == fingerprint {
			return report.DeepCopy(), nil
		}
		delete(r.openByFingerprint, fingerprint)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{LabelFingerprintHash: hash}); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports: %w", err)
	}
	if len(list.Items) == 0 {
		if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("failed to list IncidentReports fallback: %w", err)
		}
	}
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == PhaseResolved || reportFingerprint(report) != fingerprint {
			continue
		}
		r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
		return report.DeepCopy(), nil
	}
	return nil, nil
}

func (r *Reporter) findResolvableIncident(ctx context.Context, namespace, fingerprint, hash string) (*rcav1alpha1.IncidentReport, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{LabelFingerprintHash: hash}); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports for reopen check: %w", err)
	}
	if len(list.Items) == 0 {
		if err := r.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("failed to list IncidentReports for reopen fallback: %w", err)
		}
	}

	var best *rcav1alpha1.IncidentReport
	for i := range list.Items {
		report := &list.Items[i]
		resolvedAt := incidentstatus.EffectiveResolvedTime(report.Status)
		if report.Status.Phase != PhaseResolved || reportFingerprint(report) != fingerprint || resolvedAt == nil {
			continue
		}
		if r.Now().Sub(resolvedAt.Time) > ReopenWindow {
			continue
		}
		bestResolvedAt := bestResolvedTime(best)
		if best == nil || resolvedAt.After(bestResolvedAt.Time) {
			best = report.DeepCopy()
		}
	}
	return best, nil
}

// findExistingByWorkloadRef is a last-resort dedup guard for workload-scoped
// incidents. It lists all incidents in the namespace that have a matching
// WorkloadRef or a matching entry in AffectedResources, regardless of incident
// type. This ensures that different signal types (e.g. ImagePullBackOff and
// StalledRollout) targeting the same Deployment coalesce into a single
// incident. It returns the best candidate: an open incident takes priority
// over a recently-resolved one (within ReopenWindow).
func (r *Reporter) findExistingByWorkloadRef(ctx context.Context, input incident.Input) (*rcav1alpha1.IncidentReport, error) {
	workloadRef := input.Scope.WorkloadRef
	if workloadRef == nil || workloadRef.Name == "" {
		return nil, nil
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := r.client.List(ctx, list, client.InNamespace(input.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports for workload-ref fallback: %w", err)
	}

	var bestOpen *rcav1alpha1.IncidentReport
	var bestResolved *rcav1alpha1.IncidentReport

	for i := range list.Items {
		report := &list.Items[i]
		if !reportMatchesWorkloadRef(report, workloadRef) {
			continue
		}
		if report.Status.Phase != PhaseResolved {
			if bestOpen == nil {
				bestOpen = report.DeepCopy()
			}
			continue
		}
		resolvedAt := incidentstatus.EffectiveResolvedTime(report.Status)
		if resolvedAt == nil || r.Now().Sub(resolvedAt.Time) > ReopenWindow {
			continue
		}
		if bestResolved == nil || resolvedAt.After(bestResolvedTime(bestResolved).Time) {
			bestResolved = report.DeepCopy()
		}
	}

	if bestOpen != nil {
		return bestOpen, nil
	}
	return bestResolved, nil
}

// reportMatchesWorkloadRef returns true when the given incident covers the
// provided workload ref, checking both Spec.Scope.WorkloadRef and every
// entry in Status.AffectedResources. Kind, Namespace, and Name must all match.
func reportMatchesWorkloadRef(report *rcav1alpha1.IncidentReport, workloadRef *rcav1alpha1.IncidentObjectRef) bool {
	if workloadRef == nil {
		return false
	}
	if ref := report.Spec.Scope.WorkloadRef; ref != nil {
		if ref.Kind == workloadRef.Kind && ref.Namespace == workloadRef.Namespace && ref.Name == workloadRef.Name {
			return true
		}
	}
	for _, res := range report.Status.AffectedResources {
		if res.Kind == workloadRef.Kind && res.Namespace == workloadRef.Namespace && res.Name == workloadRef.Name {
			return true
		}
	}
	return false
}

func (r *Reporter) updateActiveIncident(ctx context.Context, report *rcav1alpha1.IncidentReport, input incident.Input, fingerprint, hash string) error {
	now := metav1.NewTime(input.ObservedAt)
	if report.Labels == nil {
		report.Labels = make(map[string]string)
	}
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}
	if report.Annotations[AnnotationSignalSeen] == "" && report.Status.SignalCount > 0 {
		report.Annotations[AnnotationSignalSeen] = strconv.FormatInt(report.Status.SignalCount, 10)
	}

	metaBase := report.DeepCopy()
	report.Labels[LabelSeverity] = higherSeverity(report.Labels[LabelSeverity], input.Severity)
	report.Labels[LabelIncidentType] = input.IncidentType
	report.Labels[LabelFingerprintHash] = hash
	report.Labels[LabelPodName] = safeLabelValue(primaryPodName(input))
	report.Annotations[AnnotationSignal] = input.Summary
	report.Annotations[AnnotationDedupKey] = input.DedupKey
	report.Annotations[AnnotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[AnnotationSignalSeen] = incrementCounter(report.Annotations[AnnotationSignalSeen])
	report.Spec.Fingerprint = fingerprint
	report.Spec.IncidentType = input.IncidentType
	report.Spec.Scope = input.Scope
	if err := r.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Severity = higherSeverity(report.Status.Severity, input.Severity)
	report.Status.IncidentType = input.IncidentType
	report.Status.Summary = input.Summary
	report.Status.Reason = input.Reason
	report.Status.Message = input.Message
	report.Status.LastObservedAt = &now
	report.Status.StartTime = report.Status.FirstObservedAt
	report.Status.SignalCount++
	report.Status.Timeline = incidentstatus.AppendTimeline(report.Status.Timeline, now, input.Summary)
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, input.Summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	report.Status.AffectedResources = mergeAffectedResources(report.Status.AffectedResources, input.AffectedResources)
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
	return nil
}

func (r *Reporter) reopenIncident(ctx context.Context, report *rcav1alpha1.IncidentReport, input incident.Input, fingerprint, hash string) error {
	now := metav1.NewTime(input.ObservedAt)
	if report.Labels == nil {
		report.Labels = make(map[string]string)
	}
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}

	metaBase := report.DeepCopy()
	report.Labels[LabelSeverity] = higherSeverity(report.Labels[LabelSeverity], input.Severity)
	report.Labels[LabelIncidentType] = input.IncidentType
	report.Labels[LabelFingerprintHash] = hash
	report.Annotations[AnnotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[AnnotationSignalSeen] = incrementCounter(report.Annotations[AnnotationSignalSeen])
	report.Annotations[AnnotationSignal] = input.Summary
	report.Annotations[AnnotationDedupKey] = input.DedupKey
	report.Spec.Fingerprint = fingerprint
	report.Spec.IncidentType = input.IncidentType
	report.Spec.Scope = input.Scope
	if err := r.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata on reopen: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Phase = PhaseDetecting
	report.Status.ResolvedTime = nil
	report.Status.ResolvedAt = nil
	report.Status.ActiveAt = nil
	report.Status.FirstObservedAt = &now
	report.Status.LastObservedAt = &now
	report.Status.StartTime = &now
	report.Status.Severity = higherSeverity(report.Status.Severity, input.Severity)
	report.Status.IncidentType = input.IncidentType
	report.Status.Summary = input.Summary
	report.Status.Reason = input.Reason
	report.Status.Message = input.Message
	if report.Status.SignalCount <= 0 {
		report.Status.SignalCount = 1
	} else {
		report.Status.SignalCount++
	}
	report.Status.StabilizationWindowSeconds = int64((5 * time.Minute).Seconds())
	report.Status.Timeline = incidentstatus.AppendTimeline(report.Status.Timeline, now, fmt.Sprintf("Incident re-opened: %s", input.Summary))
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, input.Summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	report.Status.AffectedResources = mergeAffectedResources(report.Status.AffectedResources, input.AffectedResources)
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status on reopen: %w", err)
	}

	r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
	metrics.RecordIncidentDetected(input.AgentRef, input.IncidentType, input.Severity)
	if r.Recorder != nil {
		r.Recorder.Eventf(report, nil, corev1.EventTypeWarning, "IncidentReopened", "Reopen",
			"Incident re-opened: %s", input.Summary)
	}
	return nil
}

func incidentAffectsPod(report *rcav1alpha1.IncidentReport, podName, namespace string) bool {
	for _, resource := range report.Status.AffectedResources {
		if resource.Kind == resourceKindPod && resource.Name == podName && resource.Namespace == namespace {
			return true
		}
	}
	return false
}

func isPodCurrentlyReady(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
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
		return defaultNameToken
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
		return defaultNameToken
	}
	return out
}

func primaryPodName(in incident.Input) string {
	for _, resource := range in.AffectedResources {
		if resource.Kind == resourceKindPod {
			return resource.Name
		}
	}
	if in.Scope.ResourceRef != nil && in.Scope.ResourceRef.Kind == resourceKindPod {
		return in.Scope.ResourceRef.Name
	}
	return ""
}

func primaryResourceName(in incident.Input) string {
	if in.Scope.WorkloadRef != nil {
		return in.Scope.WorkloadRef.Name
	}
	if in.Scope.ResourceRef != nil {
		return in.Scope.ResourceRef.Name
	}
	if len(in.AffectedResources) > 0 {
		return in.AffectedResources[0].Name
	}
	return "incident"
}

func mergeAffectedResources(existing, incoming []rcav1alpha1.AffectedResource) []rcav1alpha1.AffectedResource {
	out := append([]rcav1alpha1.AffectedResource{}, existing...)
	for _, candidate := range incoming {
		found := false
		for _, current := range out {
			if current.Kind == candidate.Kind && current.Namespace == candidate.Namespace && current.Name == candidate.Name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, candidate)
		}
	}
	return trimAffectedResources(out)
}

func trimAffectedResources(in []rcav1alpha1.AffectedResource) []rcav1alpha1.AffectedResource {
	if len(in) == 0 {
		return []rcav1alpha1.AffectedResource{}
	}
	return in
}

// reportFingerprint computes the fingerprint for an existing IncidentReport.
// The fingerprint is purely scope-based (no incident type), matching
// Input.Fingerprint(). This ensures that different signal types targeting the
// same resource share a single incident.
func reportFingerprint(report *rcav1alpha1.IncidentReport) string {
	if report == nil {
		return ""
	}
	if report.Spec.Fingerprint != "" {
		return report.Spec.Fingerprint
	}

	scope := report.Spec.Scope
	var parts []string

	switch scope.Level {
	case "Cluster":
		parts = append(parts, "Cluster")
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	case "Workload":
		parts = append(parts, "Workload")
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
		if scope.WorkloadRef != nil {
			parts = append(parts, strings.ToLower(scope.WorkloadRef.Kind), scope.WorkloadRef.Name)
		}
	case "Namespace":
		parts = append(parts, "Namespace")
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
	case "Pod":
		parts = append(parts, "Pod")
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	default:
		// Fallback for legacy incidents: derive from AffectedResources.
		for _, res := range report.Status.AffectedResources {
			switch res.Kind {
			case "Node":
				return strings.Join([]string{"Cluster", "node", res.Name}, "|")
			case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet":
				return strings.Join([]string{"Workload", res.Namespace, strings.ToLower(res.Kind), res.Name}, "|")
			case "Pod":
				return strings.Join([]string{"Pod", res.Namespace, "pod", res.Name}, "|")
			}
		}
		if report.Namespace != "" {
			return strings.Join([]string{"Namespace", report.Namespace}, "|")
		}
		return report.Name
	}

	return strings.Join(parts, "|")
}

func bestResolvedTime(report *rcav1alpha1.IncidentReport) *metav1.Time {
	if report == nil {
		return nil
	}
	return incidentstatus.EffectiveResolvedTime(report.Status)
}
