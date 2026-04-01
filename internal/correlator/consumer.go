package correlator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/metrics"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
	"github.com/gaurangkudale/rca-operator/internal/signals"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const (
	annotationDedupKey   = reporter.AnnotationDedupKey
	annotationSignal     = reporter.AnnotationSignal
	annotationLastSeen   = reporter.AnnotationLastSeen
	annotationSignalSeen = reporter.AnnotationSignalSeen

	labelAgent        = reporter.LabelAgent
	labelSeverity     = reporter.LabelSeverity
	labelIncidentType = reporter.LabelIncidentType
	labelPodName      = reporter.LabelPodName

	phaseDetecting = reporter.PhaseDetecting
	phaseActive    = reporter.PhaseActive
	phaseResolved  = reporter.PhaseResolved
	valueUnknown   = reporter.ValueUnknown

	incidentTypeNodeFailure = reporter.IncidentTypeNodeFailure

	maxTimelineEntries = reporter.MaxTimelineEntries
	maxSignalEntries   = reporter.MaxSignalEntries
)

type Consumer struct {
	client     client.Client
	events     <-chan watcher.CorrelatorEvent
	log        logr.Logger
	now        func() time.Time
	ruleEngine RuleEngine
	rep        *reporter.Reporter
	resolver   *incident.Resolver

	// Signal processing pipeline (Phase 1)
	normalizer *signals.Normalizer
	enricher   *signals.Enricher
}

func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger, opts ...Option) *Consumer {
	consumer := &Consumer{
		client:     c,
		events:     events,
		log:        logger.WithName("incident-engine-consumer"),
		now:        time.Now,
		resolver:   incident.NewResolver(c),
		normalizer: signals.NewNormalizer(nil),
		enricher:   signals.NewEnricher(c, logger),
	}
	rep := reporter.NewReporter(c, logger)
	rep.Now = func() time.Time { return consumer.now() }
	consumer.rep = rep

	for _, opt := range opts {
		opt(consumer)
	}
	return consumer
}

func (c *Consumer) Run(ctx context.Context) {
	if err := c.consolidateDuplicates(ctx); err != nil {
		c.log.Error(err, "Failed to consolidate duplicate incidents at startup")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-c.events:
			if !ok {
				return
			}
			if event == nil {
				continue
			}
			if err := c.handleEvent(ctx, event); err != nil {
				c.log.Error(err, "Could not process collected signal", "eventType", event.Type(), "dedupKey", event.DedupKey())
			}
		}
	}
}

func (c *Consumer) handleEvent(ctx context.Context, event watcher.CorrelatorEvent) error {
	if c.ruleEngine != nil {
		c.ruleEngine.Add(event)
	}

	// Lifecycle events bypass the signal pipeline.
	if healthy, ok := event.(watcher.PodHealthyEvent); ok {
		return c.rep.ResolveForHealthyPod(ctx, healthy.Namespace, healthy.PodName)
	}
	if deleted, ok := event.(watcher.PodDeletedEvent); ok {
		return c.rep.ResolveForDeletedPod(ctx, deleted.Namespace, deleted.PodName)
	}

	// ── Signal Processing Pipeline: Normalize → Enrich → Rule Engine ──
	startTime := time.Now()
	sig, ok := c.normalizer.Normalize(event)
	if !ok {
		return nil
	}
	sig = c.enricher.Enrich(ctx, sig)
	input := sig.Input

	// Record signal processing metrics.
	metrics.RecordSignalProcessed(string(event.Type()), input.AgentRef)
	metrics.ObserveSignalDuration(string(event.Type()), time.Since(startTime).Seconds())

	// ── Rule Engine evaluation ──
	if c.ruleEngine != nil {
		if result := c.ruleEngine.Evaluate(event); result.Fired {
			metrics.RecordRuleEvaluation(result.Rule, true)
			input.IncidentType = result.IncidentType
			input.Severity = result.Severity
			input.Summary = result.Summary
			input.Message = result.Summary
			if result.Resource != "" {
				switch input.IncidentType {
				case incidentTypeNodeFailure:
					input.Scope.Level = incident.ScopeLevelCluster
					input.Scope.Namespace = ""
					input.Scope.WorkloadRef = nil
					input.Scope.ResourceRef = &rcav1alpha1.IncidentObjectRef{
						APIVersion: corev1.SchemeGroupVersion.String(),
						Kind:       "Node",
						Name:       result.Resource,
					}
					input.AffectedResources = []rcav1alpha1.AffectedResource{
						{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Node", Name: result.Resource},
					}
				default:
					input.Scope.Level = incident.ScopeLevelWorkload
					input.Scope.WorkloadRef = &rcav1alpha1.IncidentObjectRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Namespace:  input.Namespace,
						Name:       result.Resource,
					}
					input.AffectedResources = mergeAffectedResources(input.AffectedResources, []rcav1alpha1.AffectedResource{
						{APIVersion: "apps/v1", Kind: "Deployment", Namespace: input.Namespace, Name: result.Resource},
					})
				}
			}
			c.log.Info("Rule engine produced incident override",
				"rule", result.Rule,
				"incidentType", input.IncidentType,
				"severity", input.Severity,
			)
		}
	}

	coalesced, err := c.coalesceOverlappingIncident(ctx, input)
	if err != nil {
		return err
	}
	input = coalesced

	return c.rep.EnsureSignal(ctx, input)
}

func (c *Consumer) consolidateDuplicates(ctx context.Context) error {
	return c.rep.Consolidate(ctx)
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
	return out
}

// coalesceOverlappingIncident detects when a new workload-scoped incident signal
// would otherwise produce a second open incident for the same workload. When an
// active (non-resolved) incident already exists for the same workload — regardless
// of incident type — the incoming signal is coalesced into that incident by
// adopting its incident type. This enforces the single-incident-per-workload rule
// without requiring hardcoded type-pair mappings.
func (c *Consumer) coalesceOverlappingIncident(ctx context.Context, input incident.Input) (incident.Input, error) {
	if input.Scope.Level != incident.ScopeLevelWorkload || input.Scope.WorkloadRef == nil {
		return input, nil
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(input.Namespace)); err != nil {
		return input, fmt.Errorf("list IncidentReports for overlap check: %w", err)
	}

	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == phaseResolved {
			continue
		}
		if !incidentMatchesWorkload(report, input.Scope.WorkloadRef) {
			continue
		}

		existingType := report.Spec.IncidentType
		if existingType == "" {
			existingType = report.Status.IncidentType
		}
		if existingType == input.IncidentType {
			// Same type — let the normal fingerprint dedup path handle it.
			return input, nil
		}

		// Different type but same workload: adopt the existing incident's type so
		// the fingerprint resolves to the same CR instead of creating a new one.
		originalType := input.IncidentType
		input.IncidentType = existingType
		input.Severity = higherSeverity(input.Severity, report.Status.Severity)
		c.log.Info("Coalesced workload signal into existing incident",
			"fromType", originalType,
			"toType", existingType,
			"namespace", input.Namespace,
			"workload", input.Scope.WorkloadRef.Name,
			"incident", report.Name,
		)
		return input, nil
	}

	return input, nil
}

func incidentMatchesWorkload(report *rcav1alpha1.IncidentReport, workload *rcav1alpha1.IncidentObjectRef) bool {
	if report == nil || workload == nil {
		return false
	}

	if ref := report.Spec.Scope.WorkloadRef; ref != nil {
		if ref.Kind == workload.Kind && ref.Namespace == workload.Namespace && ref.Name == workload.Name {
			return true
		}
	}
	if ref := report.Spec.Scope.ResourceRef; ref != nil && report.Spec.Scope.Level == incident.ScopeLevelWorkload {
		if ref.Kind == workload.Kind && ref.Namespace == workload.Namespace && ref.Name == workload.Name {
			return true
		}
	}
	for _, res := range report.Status.AffectedResources {
		if res.Kind == workload.Kind && res.Namespace == workload.Namespace && res.Name == workload.Name {
			return true
		}
	}
	return false
}

// Utility helpers kept for existing package tests.
func incidentAffectsPod(report *rcav1alpha1.IncidentReport, podName, namespace string) bool {
	for _, resource := range report.Status.AffectedResources {
		if resource.Kind == "Pod" && resource.Name == podName && resource.Namespace == namespace {
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
	if len(in) <= maxTimelineEntries {
		return in
	}
	return in[len(in)-maxTimelineEntries:]
}

func trimSignals(in []string) []string {
	if len(in) <= maxSignalEntries {
		return in
	}
	return in[len(in)-maxSignalEntries:]
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
		return valueUnknown
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
		return valueUnknown
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

func mapEvent(event watcher.CorrelatorEvent) (namespace, podName, agentRef, incidentType, severity, summary string) {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		summary = fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
		if e.LastExitCode != 0 && e.ExitCodeCategory != "" {
			summary = fmt.Sprintf("%s exitCode=%d category=%s description=%s", summary, e.LastExitCode, e.ExitCodeCategory, e.ExitCodeDescription)
		}
		return e.Namespace, e.PodName, e.AgentName, "CrashLoop", "P3", summary
	case watcher.OOMKilledEvent:
		return e.Namespace, e.PodName, e.AgentName, "OOM", "P2", fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
	case watcher.ImagePullBackOffEvent:
		return e.Namespace, e.PodName, e.AgentName, "ImagePullFailure", "P3", fmt.Sprintf("Image pull failure reason=%s", e.Reason)
	case watcher.PodPendingTooLongEvent:
		return e.Namespace, e.PodName, e.AgentName, "SchedulingFailure", "P3", fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	case watcher.GracePeriodViolationEvent:
		return e.Namespace, e.PodName, e.AgentName, "GracePeriodViolation", "P2", fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
	case watcher.NodeNotReadyEvent:
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, "P1", fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return e.Namespace, e.PodName, e.AgentName, incidentTypeNodeFailure, "P2", fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		return e.Namespace, e.DeploymentName, e.AgentName, "DeploymentRolloutFailure", "P2",
			fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
				e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
	case watcher.NodePressureEvent:
		severity := "P2"
		if e.PressureType == "PIDPressure" {
			severity = "P3"
		}
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, severity,
			fmt.Sprintf("Node %s condition=%s message=%s", e.NodeName, e.PressureType, e.Message)
	default:
		return "", "", "", "", "", ""
	}
}
