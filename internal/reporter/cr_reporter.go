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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
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
	IncidentTypeNodeFailure = "NodeFailure"
	IncidentTypeRegistry    = "Registry"
)

const (
	SignalCooldown = 5 * time.Minute
	ReopenWindow   = 30 * time.Minute
)

const (
	MaxTimelineEntries = 50
	MaxSignalEntries   = 20
)

type Reporter struct {
	client   client.Client
	log      logr.Logger
	Recorder record.EventRecorder
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
		Timeline:                   []rcav1alpha1.TimelineEvent{{Time: firstSeen, Event: input.Summary}},
	}
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
	r.log.Info("Created IncidentReport",
		"namespace", report.Namespace,
		"name", report.Name,
		"incidentType", input.IncidentType,
		"fingerprint", fingerprint,
	)
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeWarning, "IncidentDetected",
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
		report.Status.Phase = PhaseResolved
		report.Status.ResolvedTime = &now
		report.Status.ResolvedAt = &now
		report.Status.Timeline = appendTimeline(report.Status.Timeline, now, fmt.Sprintf("Pod %s became Running and Ready", podName))
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
		report.Status.Phase = PhaseResolved
		report.Status.ResolvedTime = &now
		report.Status.ResolvedAt = &now
		report.Status.Timeline = appendTimeline(report.Status.Timeline, now, fmt.Sprintf("Pod %s was deleted from the cluster", podName))
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
			extra.Status.Phase = PhaseResolved
			extra.Status.ResolvedTime = &now
			extra.Status.ResolvedAt = &now
			extra.Status.Timeline = appendTimeline(extra.Status.Timeline, now,
				fmt.Sprintf("Merged into canonical incident %s during startup consolidation", group.canonical.Name))
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
		resolvedAt := report.Status.ResolvedAt
		if resolvedAt == nil {
			resolvedAt = report.Status.ResolvedTime
		}
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
	report.Status.Timeline = appendTimeline(report.Status.Timeline, now, input.Summary)
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
	report.Status.Timeline = appendTimeline(report.Status.Timeline, now, fmt.Sprintf("Incident re-opened: %s", input.Summary))
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, input.Summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	report.Status.AffectedResources = mergeAffectedResources(report.Status.AffectedResources, input.AffectedResources)
	if err := r.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status on reopen: %w", err)
	}

	r.openByFingerprint[fingerprint] = types.NamespacedName{Namespace: report.Namespace, Name: report.Name}
	if r.Recorder != nil {
		r.Recorder.Eventf(report, corev1.EventTypeWarning, "IncidentReopened", "Incident re-opened: %s", input.Summary)
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

func trimTimeline(in []rcav1alpha1.TimelineEvent) []rcav1alpha1.TimelineEvent {
	if len(in) <= MaxTimelineEntries {
		return in
	}
	return in[len(in)-MaxTimelineEntries:]
}

func appendTimeline(in []rcav1alpha1.TimelineEvent, t metav1.Time, msg string) []rcav1alpha1.TimelineEvent {
	in = append(in, rcav1alpha1.TimelineEvent{Time: t, Event: msg})
	return trimTimeline(in)
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
	if in.Scope.ResourceRef != nil && in.Scope.ResourceRef.Kind == "Pod" {
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

func reportFingerprint(report *rcav1alpha1.IncidentReport) string {
	if report == nil {
		return ""
	}
	if report.Spec.Fingerprint != "" {
		return report.Spec.Fingerprint
	}
	incidentType := report.Spec.IncidentType
	if incidentType == "" {
		incidentType = report.Status.IncidentType
	}
	if incidentType == IncidentTypeRegistry {
		return strings.Join([]string{incidentType, report.Namespace}, "|")
	}
	switch incidentType {
	case "BadDeploy":
		if len(report.Status.AffectedResources) > 0 && report.Status.AffectedResources[0].Name != "" {
			return strings.Join([]string{incidentType, report.Status.AffectedResources[0].Namespace, "deployment", report.Status.AffectedResources[0].Name}, "|")
		}
	case IncidentTypeNodeFailure:
		if len(report.Status.AffectedResources) > 0 && report.Status.AffectedResources[0].Name != "" {
			return strings.Join([]string{incidentType, "node", report.Status.AffectedResources[0].Name}, "|")
		}
	}
	for _, res := range report.Status.AffectedResources {
		switch res.Kind {
		case "Node":
			return strings.Join([]string{incidentType, "node", res.Name}, "|")
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet":
			return strings.Join([]string{incidentType, res.Namespace, strings.ToLower(res.Kind), res.Name}, "|")
		case "Pod":
			return strings.Join([]string{incidentType, res.Namespace, "pod", res.Name}, "|")
		}
	}
	if report.Namespace != "" {
		return strings.Join([]string{incidentType, report.Namespace, report.Name}, "|")
	}
	return strings.Join([]string{incidentType, report.Name}, "|")
}

func bestResolvedTime(report *rcav1alpha1.IncidentReport) *metav1.Time {
	if report == nil {
		return nil
	}
	if report.Status.ResolvedAt != nil {
		return report.Status.ResolvedAt
	}
	return report.Status.ResolvedTime
}
