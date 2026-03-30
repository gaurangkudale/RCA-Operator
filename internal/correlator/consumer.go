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
	"github.com/gaurangkudale/rca-operator/internal/reporter"
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
	incidentTypeRegistry    = reporter.IncidentTypeRegistry
	incidentTypeBadDeploy   = "BadDeploy"

	maxTimelineEntries = reporter.MaxTimelineEntries
	maxSignalEntries   = reporter.MaxSignalEntries
)

type Consumer struct {
	client     client.Client
	events     <-chan watcher.CorrelatorEvent
	log        logr.Logger
	now        func() time.Time
	correlator *Correlator
	rep        *reporter.Reporter
	resolver   *incident.Resolver
}

func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger, opts ...Option) *Consumer {
	consumer := &Consumer{
		client:   c,
		events:   events,
		log:      logger.WithName("correlator-consumer"),
		now:      time.Now,
		resolver: incident.NewResolver(c),
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
	if err := c.consolidateRegistryDuplicates(ctx); err != nil {
		c.log.Error(err, "Failed to consolidate duplicate Registry incidents at startup")
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
				c.log.Error(err, "Could not process watcher event", "eventType", event.Type(), "dedupKey", event.DedupKey())
			}
		}
	}
}

func (c *Consumer) handleEvent(ctx context.Context, event watcher.CorrelatorEvent) error {
	if c.correlator != nil {
		c.correlator.Add(event)
	}

	if healthy, ok := event.(watcher.PodHealthyEvent); ok {
		return c.rep.ResolveForHealthyPod(ctx, healthy.Namespace, healthy.PodName)
	}
	if deleted, ok := event.(watcher.PodDeletedEvent); ok {
		return c.rep.ResolveForDeletedPod(ctx, deleted.Namespace, deleted.PodName)
	}

	input, ok := c.mapEvent(ctx, event)
	if !ok {
		return nil
	}

	if c.correlator != nil {
		if result := c.correlator.Evaluate(event); result.Fired {
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
			c.log.Info("Correlation rule fired",
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

func (c *Consumer) consolidateRegistryDuplicates(ctx context.Context) error {
	return c.rep.Consolidate(ctx)
}

func (c *Consumer) mapEvent(ctx context.Context, event watcher.CorrelatorEvent) (incident.Input, bool) {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		summary := fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
		if e.LastExitCode != 0 && e.ExitCodeCategory != "" {
			summary = fmt.Sprintf("%s exitCode=%d category=%s description=%s", summary, e.LastExitCode, e.ExitCodeCategory, e.ExitCodeDescription)
		}
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, "CrashLoop", "P3", "CrashLoopBackOff", summary, event), true
	case watcher.OOMKilledEvent:
		summary := fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, "OOM", "P2", e.Reason, summary, event), true
	case watcher.ImagePullBackOffEvent:
		summary := fmt.Sprintf("Image pull failure reason=%s message=%s", e.Reason, e.Message)
		input := c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, incidentTypeRegistry, "P3", e.Reason, summary, event)
		input = promoteRegistryScope(input, e.Namespace, e.PodName)
		return input, true
	case watcher.PodPendingTooLongEvent:
		summary := fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, incidentTypeBadDeploy, "P3", "PendingTooLong", summary, event), true
	case watcher.GracePeriodViolationEvent:
		summary := fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, "GracePeriodViolation", "P2", "GracePeriodExceeded", summary, event), true
	case watcher.NodeNotReadyEvent:
		return incident.Input{
			Namespace:    e.Namespace,
			AgentRef:     e.AgentName,
			IncidentType: incidentTypeNodeFailure,
			Severity:     "P1",
			Summary:      fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message),
			Reason:       e.Reason,
			Message:      e.Message,
			DedupKey:     event.DedupKey(),
			ObservedAt:   event.OccurredAt(),
			Scope: rcav1alpha1.IncidentScope{
				Level: incident.ScopeLevelCluster,
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: corev1.SchemeGroupVersion.String(),
					Kind:       "Node",
					Name:       e.NodeName,
				},
			},
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Node", Name: e.NodeName},
			},
		}, true
	case watcher.PodEvictedEvent:
		summary := fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, incidentTypeNodeFailure, "P2", e.Reason, summary, event), true
	case watcher.ProbeFailureEvent:
		summary := fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", e.ProbeType, summary, event), true
	case watcher.StalledRolloutEvent:
		summary := fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
			e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
		return incident.Input{
			Namespace:    e.Namespace,
			AgentRef:     e.AgentName,
			IncidentType: incidentTypeBadDeploy,
			Severity:     "P2",
			Summary:      summary,
			Reason:       e.Reason,
			Message:      e.Message,
			DedupKey:     event.DedupKey(),
			ObservedAt:   event.OccurredAt(),
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: e.Namespace,
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  e.Namespace,
					Name:       e.DeploymentName,
				},
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  e.Namespace,
					Name:       e.DeploymentName,
				},
			},
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: e.Namespace, Name: e.DeploymentName},
			},
		}, true
	case watcher.NodePressureEvent:
		severity := "P2"
		if e.PressureType == "PIDPressure" {
			severity = "P3"
		}
		return incident.Input{
			Namespace:    e.Namespace,
			AgentRef:     e.AgentName,
			IncidentType: incidentTypeNodeFailure,
			Severity:     severity,
			Summary:      fmt.Sprintf("Node %s condition=%s message=%s", e.NodeName, e.PressureType, e.Message),
			Reason:       e.PressureType,
			Message:      e.Message,
			DedupKey:     event.DedupKey(),
			ObservedAt:   event.OccurredAt(),
			Scope: rcav1alpha1.IncidentScope{
				Level: incident.ScopeLevelCluster,
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: corev1.SchemeGroupVersion.String(),
					Kind:       "Node",
					Name:       e.NodeName,
				},
			},
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Node", Name: e.NodeName},
			},
		}, true
	case watcher.CPUThrottlingEvent:
		summary := fmt.Sprintf("CPU throttling high container=%s message=%s", e.ContainerName, e.Message)
		return c.podScopedInput(ctx, e.Namespace, e.PodName, e.AgentName, "ResourceSaturation", "P3", "CPUThrottlingHigh", summary, event), true
	default:
		return incident.Input{}, false
	}
}

func (c *Consumer) podScopedInput(
	ctx context.Context,
	namespace, podName, agentRef, incidentType, severity, reason, summary string,
	event watcher.CorrelatorEvent,
) incident.Input {
	scope, affectedResources, err := c.resolver.ResolvePodScope(ctx, namespace, podName)
	if err != nil {
		c.log.V(1).Info("Falling back to pod scope for incident", "namespace", namespace, "pod", podName, "error", err.Error())
	}
	return incident.Input{
		Namespace:         namespace,
		AgentRef:          agentRef,
		IncidentType:      incidentType,
		Severity:          severity,
		Summary:           summary,
		Reason:            reason,
		Message:           summary,
		DedupKey:          event.DedupKey(),
		ObservedAt:        event.OccurredAt(),
		Scope:             scope,
		AffectedResources: affectedResources,
	}
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

func promoteRegistryScope(input incident.Input, namespace, podName string) incident.Input {
	if input.Scope.WorkloadRef != nil && input.Scope.WorkloadRef.Name != "" {
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = namespace
		input.Scope.ResourceRef = input.Scope.WorkloadRef
		return input
	}

	workloadName := guessDeploymentNameFromPod(podName)
	if workloadName == "" {
		input.Scope.Level = incident.ScopeLevelNamespace
		input.Scope.Namespace = namespace
		input.Scope.WorkloadRef = nil
		input.Scope.ResourceRef = nil
		return input
	}

	workloadRef := &rcav1alpha1.IncidentObjectRef{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  namespace,
		Name:       workloadName,
	}
	input.Scope.Level = incident.ScopeLevelWorkload
	input.Scope.Namespace = namespace
	input.Scope.WorkloadRef = workloadRef
	input.Scope.ResourceRef = workloadRef
	input.AffectedResources = mergeAffectedResources(input.AffectedResources, []rcav1alpha1.AffectedResource{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: workloadName},
	})
	return input
}

func guessDeploymentNameFromPod(podName string) string {
	parts := strings.Split(strings.TrimSpace(podName), "-")
	if len(parts) < 2 {
		return ""
	}

	last := parts[len(parts)-1]
	if len(last) == 5 {
		if len(parts) >= 3 && looksLikeReplicaSetHash(parts[len(parts)-2]) {
			return strings.Join(parts[:len(parts)-2], "-")
		}
		return strings.Join(parts[:len(parts)-1], "-")
	}

	return ""
}

func looksLikeReplicaSetHash(token string) bool {
	if len(token) < 8 || len(token) > 10 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func (c *Consumer) coalesceOverlappingIncident(ctx context.Context, input incident.Input) (incident.Input, error) {
	if input.Scope.Level != incident.ScopeLevelWorkload || input.Scope.WorkloadRef == nil {
		return input, nil
	}

	var overlappingType string
	switch input.IncidentType {
	case incidentTypeRegistry:
		overlappingType = incidentTypeBadDeploy
	case incidentTypeBadDeploy:
		overlappingType = incidentTypeRegistry
	default:
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
		reportType := report.Spec.IncidentType
		if reportType == "" {
			reportType = report.Status.IncidentType
		}
		if reportType != overlappingType {
			continue
		}
		if !incidentMatchesWorkload(report, input.Scope.WorkloadRef) {
			continue
		}

		originalType := input.IncidentType
		input.IncidentType = overlappingType
		input.Severity = higherSeverity(input.Severity, report.Status.Severity)
		c.log.Info("Coalesced overlapping incident into existing workload incident",
			"fromType", originalType,
			"toType", overlappingType,
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
		return e.Namespace, e.PodName, e.AgentName, "Registry", "P3", fmt.Sprintf("Image pull failure reason=%s", e.Reason)
	case watcher.PodPendingTooLongEvent:
		return e.Namespace, e.PodName, e.AgentName, incidentTypeBadDeploy, "P3", fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	case watcher.GracePeriodViolationEvent:
		return e.Namespace, e.PodName, e.AgentName, "GracePeriodViolation", "P2", fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
	case watcher.NodeNotReadyEvent:
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, "P1", fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return e.Namespace, e.PodName, e.AgentName, incidentTypeNodeFailure, "P2", fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		return e.Namespace, e.DeploymentName, e.AgentName, incidentTypeBadDeploy, "P2",
			fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
				e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
	case watcher.NodePressureEvent:
		severity := "P2"
		if e.PressureType == "PIDPressure" {
			severity = "P3"
		}
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, severity,
			fmt.Sprintf("Node %s condition=%s message=%s", e.NodeName, e.PressureType, e.Message)
	case watcher.CPUThrottlingEvent:
		return e.Namespace, e.PodName, e.AgentName, "ResourceSaturation", "P3",
			fmt.Sprintf("CPU throttling high container=%s message=%s", e.ContainerName, e.Message)
	default:
		return "", "", "", "", "", ""
	}
}
