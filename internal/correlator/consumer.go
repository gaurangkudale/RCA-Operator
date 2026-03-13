package correlator

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
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// const defaultDedupCooldown = 2 * time.Minute

const (
	annotationDedupKey   = "rca.rca-operator.io/dedup-key"
	annotationSignal     = "rca.rca-operator.io/signal"
	annotationLastSeen   = "rca.rca-operator.io/last-seen"
	annotationSignalSeen = "rca.rca-operator.io/signal-count"

	labelAgent        = "rca.rca-operator.io/agent"
	labelSeverity     = "rca.rca-operator.io/severity"
	labelIncidentType = "rca.rca-operator.io/incident-type"
	labelPodName      = "rca.rca-operator.io/pod"

	phaseActive   = "Active"
	phaseResolved = "Resolved"
	valueUnknown  = "unknown"

	maxTimelineEntries = 50
	maxSignalEntries   = 20
)

// Consumer reads watcher events, performs deduplication, and writes IncidentReport CRs.
type Consumer struct {
	client client.Client
	events <-chan watcher.CorrelatorEvent
	log    logr.Logger
	now    func() time.Time
}

// NewConsumer returns a correlator consumer with a sensible default dedup cooldown.
func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger) *Consumer {
	return &Consumer{
		client: c,
		events: events,
		log:    logger.WithName("correlator-consumer"),
		now:    time.Now,
	}
}

// Run blocks until context cancellation and consumes events continuously.
func (c *Consumer) Run(ctx context.Context) {
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
	if healthy, ok := event.(watcher.PodHealthyEvent); ok {
		return c.resolveIncidentsForPod(ctx, healthy)
	}
	if deleted, ok := event.(watcher.PodDeletedEvent); ok {
		return c.resolveIncidentsForDeletedPod(ctx, deleted)
	}

	namespace, podName, agentRef, incidentType, severity, summary := mapEvent(event)
	if namespace == "" || podName == "" {
		return nil
	}

	active, err := c.findActiveIncidentForPodType(ctx, namespace, podName, incidentType)
	if err != nil {
		return err
	}
	if active != nil {
		return c.updateActiveIncident(ctx, active, event, severity, summary)
	}

	if agentRef == "" {
		agentRef = "unknown-agent"
	}

	occurredAt := event.OccurredAt()
	if occurredAt.IsZero() {
		occurredAt = c.now()
	}
	startTime := metav1.NewTime(occurredAt)

	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s-", strings.ToLower(incidentType), safeNameToken(podName)),
			Namespace:    namespace,
			Labels: map[string]string{
				labelAgent:        agentRef,
				labelSeverity:     severity,
				labelIncidentType: incidentType,
				labelPodName:      safeLabelValue(podName),
			},
			Annotations: map[string]string{
				annotationSignal:     summary,
				annotationDedupKey:   event.DedupKey(),
				annotationLastSeen:   startTime.Format(time.RFC3339),
				annotationSignalSeen: "1",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: agentRef},
	}

	if err := c.client.Create(ctx, report); err != nil {
		return fmt.Errorf("failed to create IncidentReport: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status = rcav1alpha1.IncidentReportStatus{
		Severity:     severity,
		Phase:        phaseActive,
		IncidentType: incidentType,
		StartTime:    &startTime,
		ResolvedTime: nil,
		Notified:     false,
		AffectedResources: []rcav1alpha1.AffectedResource{
			{
				Kind:      "Pod",
				Name:      podName,
				Namespace: namespace,
			},
		},
		CorrelatedSignals: []string{summary},
		Timeline: []rcav1alpha1.TimelineEvent{
			{Time: startTime, Event: summary},
		},
		RootCause: "",
	}
	if err := c.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	c.log.Info("Created IncidentReport from watcher event",
		"namespace", namespace,
		"name", report.Name,
		"eventType", event.Type(),
		"incidentType", incidentType,
		"severity", severity,
	)
	return nil
}

func (c *Consumer) findActiveIncidentForPodType(ctx context.Context, namespace, podName, incidentType string) (*rcav1alpha1.IncidentReport, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports: %w", err)
	}
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase != phaseActive {
			continue
		}
		if report.Status.IncidentType != incidentType {
			continue
		}
		if !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		copy := report.DeepCopy()
		return copy, nil
	}
	return nil, nil
}

func mapEvent(event watcher.CorrelatorEvent) (namespace, podName, agentRef, incidentType, severity, summary string) {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		summary := fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
		// Include exit code diagnostic info if available.
		if e.LastExitCode != 0 && e.ExitCodeCategory != "" {
			summary = fmt.Sprintf("%s exitCode=%d category=%s description=%s", summary, e.LastExitCode, e.ExitCodeCategory, e.ExitCodeDescription)
		}
		return e.Namespace, e.PodName, e.AgentName, "CrashLoop", "P3", summary
	case watcher.OOMKilledEvent:
		return e.Namespace, e.PodName, e.AgentName, "OOM", "P2", fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
	case watcher.ImagePullBackOffEvent:
		return e.Namespace, e.PodName, e.AgentName, "Registry", "P3", fmt.Sprintf("Image pull failure reason=%s", e.Reason)
	case watcher.PodPendingTooLongEvent:
		// Pending can be caused by scheduling/capacity/image/constraints; treat as bad deployment signal for now.
		return e.Namespace, e.PodName, e.AgentName, "BadDeploy", "P3", fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	case watcher.GracePeriodViolationEvent:
		return e.Namespace, e.PodName, e.AgentName, "GracePeriodViolation", "P2", fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
	case watcher.NodeNotReadyEvent:
		// Node-level incident: no pod name, use node name as primary resource identifier.
		return e.Namespace, e.NodeName, e.AgentName, "NodeFailure", "P1", fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return e.Namespace, e.PodName, e.AgentName, "NodeFailure", "P2", fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		// The deployment name travels in both DeploymentName and BaseEvent.PodName
		// so the standard correlator routing path (findActiveIncidentForPodType,
		// label indexing, dedup key) works without a separate code path.
		return e.Namespace, e.DeploymentName, e.AgentName, "BadDeploy", "P2",
			fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
				e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
	case watcher.PodHealthyEvent:
		return "", "", "", "", "", ""
	default:
		return "", "", "", "", "", ""
	}
}

func (c *Consumer) resolveIncidentsForPod(ctx context.Context, event watcher.PodHealthyEvent) error {
	currentPod := &corev1.Pod{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: event.Namespace, Name: event.PodName}, currentPod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to fetch pod for resolve check: %w", err)
	}
	if !isPodCurrentlyReady(currentPod) {
		// Ignore stale healthy signals when pod is not currently Running+Ready.
		return nil
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(event.Namespace)); err != nil {
		return fmt.Errorf("failed to list IncidentReports for resolve: %w", err)
	}

	now := metav1.NewTime(c.now())
	resolvedCount := 0
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase != phaseActive {
			continue
		}
		if !incidentAffectsPod(report, event.PodName, event.Namespace) {
			continue
		}

		base := report.DeepCopy()
		report.Status.Phase = phaseResolved
		report.Status.ResolvedTime = &now
		report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
			Time:  now,
			Event: fmt.Sprintf("Pod %s became Running and Ready", event.PodName),
		})
		report.Status.Timeline = trimTimeline(report.Status.Timeline)

		if err := c.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status: %w", err)
		}
		resolvedCount++
	}

	if resolvedCount > 0 {
		c.log.Info("Resolved IncidentReports from pod healthy signal",
			"namespace", event.Namespace,
			"pod", event.PodName,
			"count", resolvedCount,
		)
	}

	return nil
}

// resolveIncidentsForDeletedPod marks all Active incidents referencing the deleted pod as Resolved.
func (c *Consumer) resolveIncidentsForDeletedPod(ctx context.Context, event watcher.PodDeletedEvent) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(event.Namespace)); err != nil {
		return fmt.Errorf("failed to list IncidentReports for deleted-pod resolve: %w", err)
	}

	now := metav1.NewTime(c.now())
	resolvedCount := 0
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase != phaseActive {
			continue
		}
		if !incidentAffectsPod(report, event.PodName, event.Namespace) {
			continue
		}

		base := report.DeepCopy()
		report.Status.Phase = phaseResolved
		report.Status.ResolvedTime = &now
		report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
			Time:  now,
			Event: fmt.Sprintf("Pod %s was deleted from the cluster", event.PodName),
		})
		report.Status.Timeline = trimTimeline(report.Status.Timeline)

		if err := c.client.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("failed to patch IncidentReport resolve status for deleted pod: %w", err)
		}
		resolvedCount++
	}

	if resolvedCount > 0 {
		c.log.Info("Resolved IncidentReports for deleted pod",
			"namespace", event.Namespace,
			"pod", event.PodName,
			"count", resolvedCount,
		)
	}

	return nil
}

func (c *Consumer) updateActiveIncident(
	ctx context.Context,
	report *rcav1alpha1.IncidentReport,
	event watcher.CorrelatorEvent,
	severity string,
	summary string,
) error {
	now := c.now()
	nowTime := metav1.NewTime(now)

	if report.Labels == nil {
		report.Labels = make(map[string]string)
	}
	if report.Annotations == nil {
		report.Annotations = make(map[string]string)
	}

	metaBase := report.DeepCopy()
	report.Labels[labelSeverity] = higherSeverity(report.Labels[labelSeverity], severity)
	report.Annotations[annotationSignal] = summary
	report.Annotations[annotationDedupKey] = event.DedupKey()
	report.Annotations[annotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[annotationSignalSeen] = incrementCounter(report.Annotations[annotationSignalSeen])
	if err := c.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Severity = higherSeverity(report.Status.Severity, severity)
	report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{Time: nowTime, Event: summary})
	report.Status.Timeline = trimTimeline(report.Status.Timeline)
	report.Status.CorrelatedSignals = append(report.Status.CorrelatedSignals, summary)
	report.Status.CorrelatedSignals = trimSignals(report.Status.CorrelatedSignals)
	if err := c.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status: %w", err)
	}

	c.log.Info("Updated active IncidentReport from repeated watcher signal",
		"namespace", report.Namespace,
		"name", report.Name,
		"eventType", event.Type(),
	)

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
