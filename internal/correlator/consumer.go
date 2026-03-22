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
	"github.com/gaurangkudale/rca-operator/internal/reporter"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// Local aliases for reporter constants so that tests in this package can
// reference them without importing the reporter package directly.
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

	maxTimelineEntries = reporter.MaxTimelineEntries
	maxSignalEntries   = reporter.MaxSignalEntries
)

// Consumer reads watcher events, performs deduplication, and writes IncidentReport CRs
// by delegating to its embedded Reporter.
type Consumer struct {
	client          client.Client
	events          <-chan watcher.CorrelatorEvent
	log             logr.Logger
	now             func() time.Time
	correlator      *Correlator      // optional; nil disables multi-event correlation
	anomalyDetector *AnomalyDetector // optional; nil disables anomaly detection
	rep             *reporter.Reporter
}

// NewConsumer returns a correlator consumer. Pass functional options (e.g.
// WithCorrelator) to enable optional features. Existing callers that pass no
// options continue to work unchanged.
func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger, opts ...Option) *Consumer {
	consumer := &Consumer{
		client: c,
		events: events,
		log:    logger.WithName("correlator-consumer"),
		now:    time.Now,
	}
	rep := reporter.NewReporter(c, logger)
	// Wire the clock so that tests overriding consumer.now automatically
	// influence all reporter timing (cooldown, reopen window, timestamps).
	rep.Now = func() time.Time { return consumer.now() }
	consumer.rep = rep

	for _, opt := range opts {
		opt(consumer)
	}
	return consumer
}

// Run blocks until context cancellation and consumes events continuously.
func (c *Consumer) Run(ctx context.Context) {
	// Merge any duplicate open Registry incidents left over from a previous run
	// (e.g. caused by bootstrap-scan API-latency races) and populate the
	// in-memory dedup cache with the surviving canonical incident per namespace.
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
	// Add to the correlation buffer before any early-return paths so that
	// healthy and deleted events are also available for rule evaluation
	// (e.g. Rule 4 uses PodHealthyEvent to detect prior pull success).
	if c.correlator != nil {
		c.correlator.Add(event)
	}

	if healthy, ok := event.(watcher.PodHealthyEvent); ok {
		return c.rep.ResolveForHealthyPod(ctx, healthy.Namespace, healthy.PodName)
	}
	if deleted, ok := event.(watcher.PodDeletedEvent); ok {
		return c.rep.ResolveForDeletedPod(ctx, deleted.Namespace, deleted.PodName)
	}

	namespace, podName, agentRef, incidentType, severity, summary := mapEvent(event)

	// Track detection method for root cause attribution
	detectionMethod := ""
	confidence := ""
	rootCause := ""

	// Run multi-event correlation rules. If a rule fires, override the
	// single-event classification with the correlated type and severity.
	if c.correlator != nil {
		if result := c.correlator.Evaluate(event); result.Fired {
			incidentType = result.IncidentType
			severity = result.Severity
			summary = result.Summary
			detectionMethod = "Rule"
			// Some rules (2, 3, 5) produce incidents scoped to a shared resource
			// (deployment, node) rather than the individual pod that triggered
			// the event. Override podName with the canonical resource identifier
			// so the dedup path uses a consistent key.
			if result.Resource != "" {
				podName = result.Resource
			}
			c.log.Info("Correlation rule fired",
				"rule", result.Rule,
				"incidentType", incidentType,
				"severity", severity,
			)
		}
	}

	// If no rule matched, try anomaly detection for unknown patterns
	if detectionMethod == "" && c.anomalyDetector != nil {
		if anomalyResult := c.anomalyDetector.Analyze(event); anomalyResult.Detected {
			incidentType = anomalyResult.Category
			severity = anomalyResult.Severity
			summary = anomalyResult.RootCause
			rootCause = anomalyResult.RootCause
			confidence = anomalyResult.Confidence
			detectionMethod = "AnomalyDetector"
			// Some anomalies (FrequencySpike) are scoped to namespace
			if anomalyResult.Resource != "" {
				podName = anomalyResult.Resource
			}
			c.log.Info("Anomaly detected",
				"category", anomalyResult.Category,
				"confidence", anomalyResult.Confidence,
				"rootCause", anomalyResult.RootCause,
			)
		}
	}

	if namespace == "" || podName == "" {
		return nil
	}

	// Use extended EnsureIncident if we have root cause info
	if rootCause != "" || detectionMethod != "" || confidence != "" {
		return c.rep.EnsureIncidentWithRCA(ctx, namespace, podName, agentRef, incidentType, severity, summary, event.DedupKey(), event.OccurredAt(), rootCause, detectionMethod, confidence)
	}

	return c.rep.EnsureIncident(ctx, namespace, podName, agentRef, incidentType, severity, summary, event.DedupKey(), event.OccurredAt())
}

// consolidateRegistryDuplicates delegates to the reporter's Consolidate method.
// It is called once at startup to merge duplicate open Registry incidents that
// may have been created during a previous run's bootstrap-scan race.
func (c *Consumer) consolidateRegistryDuplicates(ctx context.Context) error {
	return c.rep.Consolidate(ctx)
}

func mapEvent(event watcher.CorrelatorEvent) (namespace, podName, agentRef, incidentType, severity, summary string) {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		s := fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
		if e.LastExitCode != 0 && e.ExitCodeCategory != "" {
			s = fmt.Sprintf("%s exitCode=%d category=%s description=%s", s, e.LastExitCode, e.ExitCodeCategory, e.ExitCodeDescription)
		}
		return e.Namespace, e.PodName, e.AgentName, "CrashLoop", "P3", s
	case watcher.OOMKilledEvent:
		return e.Namespace, e.PodName, e.AgentName, "OOM", "P2", fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
	case watcher.ImagePullBackOffEvent:
		return e.Namespace, e.PodName, e.AgentName, "Registry", "P3", fmt.Sprintf("Image pull failure reason=%s", e.Reason)
	case watcher.PodPendingTooLongEvent:
		return e.Namespace, e.PodName, e.AgentName, "BadDeploy", "P3", fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	case watcher.GracePeriodViolationEvent:
		return e.Namespace, e.PodName, e.AgentName, "GracePeriodViolation", "P2", fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
	case watcher.NodeNotReadyEvent:
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, "P1", fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return e.Namespace, e.PodName, e.AgentName, incidentTypeNodeFailure, "P2", fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		return e.Namespace, e.DeploymentName, e.AgentName, "BadDeploy", "P2",
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
	case watcher.PodHealthyEvent:
		return "", "", "", "", "", ""
	default:
		return "", "", "", "", "", ""
	}
}

// ── Utility helpers ──────────────────────────────────────────────────────────
// These small utilities are kept here (mirroring the reporter package) so that
// tests in this package can exercise them without importing an internal package.

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
