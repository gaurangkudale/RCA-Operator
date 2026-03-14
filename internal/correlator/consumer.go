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

	phaseDetecting = "Detecting"
	phaseActive    = "Active"
	phaseResolved  = "Resolved"
	valueUnknown   = "unknown"

	incidentTypeNodeFailure = "NodeFailure"
	incidentTypeRegistry    = "Registry"

	// signalCooldown is the minimum time after the last watcher signal before an
	// incident may be resolved by a PodHealthy event. It prevents pods that briefly
	// restart between failure cycles (OOMKilled, CrashLoop) from being marked
	// Resolved immediately, which would cause a new incident to be created on the
	// next failure.
	signalCooldown = 5 * time.Minute

	// reopenWindow is the maximum age of a Resolved incident that will be reopened
	// (transitioned back to Detecting) when a new signal arrives for the same
	// pod and incident type. Resolved incidents older than this window are left
	// alone and a fresh IncidentReport is created instead.
	reopenWindow = 30 * time.Minute

	maxTimelineEntries = 50
	maxSignalEntries   = 20
)

// Consumer reads watcher events, performs deduplication, and writes IncidentReport CRs.
type Consumer struct {
	client     client.Client
	events     <-chan watcher.CorrelatorEvent
	log        logr.Logger
	now        func() time.Time
	correlator *Correlator // optional; nil disables multi-event correlation

	// openRegistryByNS is a best-effort in-memory cache: namespace → name of the
	// canonical open Registry IncidentReport. Populated on creation/reopen and
	// consulted before the API list to avoid bootstrap-scan race conditions where
	// rapid back-to-back events arrive before the just-created incident is visible
	// in the informer cache.
	openRegistryByNS map[string]string
}

// NewConsumer returns a correlator consumer. Pass functional options (e.g.
// WithCorrelator) to enable optional features. Existing callers that pass no
// options continue to work unchanged.
func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger, opts ...Option) *Consumer {
	consumer := &Consumer{
		client:           c,
		events:           events,
		log:              logger.WithName("correlator-consumer"),
		now:              time.Now,
		openRegistryByNS: make(map[string]string),
	}
	for _, opt := range opts {
		opt(consumer)
	}
	return consumer
}

// Run blocks until context cancellation and consumes events continuously.
func (c *Consumer) Run(ctx context.Context) {
	// Merge any duplicate open Registry incidents left over from a previous run
	// (e.g. caused by bootstrap-scan API-latency races) and populate the in-memory
	// dedup cache with the surviving canonical incident per namespace.
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
	// Add to the correlation buffer before any early-return paths so that healthy
	// and deleted events are also available for rule evaluation (e.g. Rule 4 uses
	// PodHealthyEvent to detect prior pull success).
	if c.correlator != nil {
		c.correlator.Add(event)
	}

	if healthy, ok := event.(watcher.PodHealthyEvent); ok {
		return c.resolveIncidentsForPod(ctx, healthy)
	}
	if deleted, ok := event.(watcher.PodDeletedEvent); ok {
		return c.resolveIncidentsForDeletedPod(ctx, deleted)
	}

	namespace, podName, agentRef, incidentType, severity, summary := mapEvent(event)

	// Run multi-event correlation rules. If a rule fires, override the single-event
	// classification with the correlated incident type and escalated severity.
	if c.correlator != nil {
		if result := c.correlator.Evaluate(event); result.Fired {
			incidentType = result.IncidentType
			severity = result.Severity
			summary = result.Summary
			// Some rules (2, 3, 5) produce incidents scoped to a shared resource
			// (deployment, node) rather than the individual pod that triggered the
			// event. Override podName with the canonical resource identifier so
			// findOpenIncident / findResolvableIncident / the new incident all use
			// the same dedup key, preventing duplicate IncidentReports.
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
	if namespace == "" || podName == "" {
		return nil
	}

	active, err := c.findOpenIncident(ctx, namespace, podName, incidentType)
	if err != nil {
		return err
	}
	if active != nil {
		return c.updateActiveIncident(ctx, active, event, podName, severity, summary)
	}

	// No open incident. Check whether there is a recently-resolved one that
	// should be reopened rather than creating a fresh IncidentReport.
	resolved, err := c.findResolvableIncident(ctx, namespace, podName, incidentType)
	if err != nil {
		return err
	}
	if resolved != nil {
		return c.reopenIncident(ctx, resolved, event, podName, severity, summary)
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
	// Populate the Registry dedup cache immediately after creation so that
	// back-to-back bootstrap-scan events for other pods in the same namespace
	// find this incident without waiting for the informer cache to catch up.
	if incidentType == incidentTypeRegistry {
		c.openRegistryByNS[namespace] = report.Name
	}

	statusBase := report.DeepCopy()
	report.Status = rcav1alpha1.IncidentReportStatus{
		Severity:     severity,
		Phase:        phaseDetecting,
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

// findOpenIncident returns the first non-Resolved IncidentReport (Detecting or
// Active) for the given pod and incident type, or nil if none exists.
//
// Registry incidents are namespace-scoped: all pods that fail to pull the same
// image consolidate into a single report, so the pod-name check is skipped for
// that type. An in-memory cache (openRegistryByNS) is checked first to avoid
// API informer-cache latency during rapid bootstrap-scan event bursts.
func (c *Consumer) findOpenIncident(ctx context.Context, namespace, podName, incidentType string) (*rcav1alpha1.IncidentReport, error) {
	// Fast path for Registry: consult the in-memory cache before hitting the API.
	if incidentType == incidentTypeRegistry {
		if name, ok := c.openRegistryByNS[namespace]; ok {
			r := &rcav1alpha1.IncidentReport{}
			if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, r); err == nil {
				if r.Status.Phase != phaseResolved {
					return r.DeepCopy(), nil
				}
			}
			// Cache is stale (incident resolved or deleted); fall through to list.
			delete(c.openRegistryByNS, namespace)
		}
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports: %w", err)
	}
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase == phaseResolved {
			continue
		}
		if report.Status.IncidentType != incidentType {
			continue
		}
		// Registry incidents are namespace-scoped: one report per namespace for
		// image-pull failures — all pods from the same (broken) deployment share it.
		if incidentType == incidentTypeRegistry {
			c.openRegistryByNS[namespace] = report.Name // refresh cache
			copy := report.DeepCopy()
			return copy, nil
		}
		if !incidentAffectsPod(report, podName, namespace) {
			continue
		}
		copy := report.DeepCopy()
		return copy, nil
	}
	return nil, nil
}

// findResolvableIncident returns the most recently resolved IncidentReport for
// the given pod and incident type, provided it was resolved within reopenWindow.
// Registry incidents are namespace-scoped: pod name is ignored and the most
// recent resolved Registry incident in the namespace is matched.
func (c *Consumer) findResolvableIncident(ctx context.Context, namespace, podName, incidentType string) (*rcav1alpha1.IncidentReport, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list IncidentReports for reopen check: %w", err)
	}

	var best *rcav1alpha1.IncidentReport
	for i := range list.Items {
		report := &list.Items[i]
		if report.Status.Phase != phaseResolved {
			continue
		}
		if report.Status.IncidentType != incidentType {
			continue
		}
		if report.Status.ResolvedTime == nil {
			continue
		}
		// Skip incidents resolved too long ago.
		if c.now().Sub(report.Status.ResolvedTime.Time) > reopenWindow {
			continue
		}
		// Scope check: Registry is namespace-wide; everything else is pod-specific.
		if incidentType != incidentTypeRegistry {
			if !incidentAffectsPod(report, podName, namespace) {
				continue
			}
		}
		// Keep the most recently resolved match.
		if best == nil || report.Status.ResolvedTime.After(best.Status.ResolvedTime.Time) {
			copy := report.DeepCopy()
			best = copy
		}
	}
	return best, nil
}

// consolidateRegistryDuplicates is called once at startup. It finds all open
// Registry IncidentReports per namespace, keeps the oldest (canonical), merges
// the AffectedResources from duplicates into it, and marks duplicates Resolved.
// This cleans up incidents created during a previous run's bootstrap-scan race
// (rare, but possible when multiple pods signal ImagePullBackOff simultaneously
// before the informer cache reflects the first created incident).
func (c *Consumer) consolidateRegistryDuplicates(ctx context.Context) error {
	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list,
		client.MatchingLabels{labelIncidentType: incidentTypeRegistry},
	); err != nil {
		return fmt.Errorf("consolidate registry: list failed: %w", err)
	}

	// Group open incidents by namespace.
	type nsGroup struct {
		canonical *rcav1alpha1.IncidentReport
		extras    []*rcav1alpha1.IncidentReport
	}
	groups := make(map[string]*nsGroup)
	for i := range list.Items {
		r := &list.Items[i]
		if r.Status.Phase == phaseResolved {
			continue
		}
		g, ok := groups[r.Namespace]
		if !ok {
			groups[r.Namespace] = &nsGroup{canonical: r}
			continue
		}
		// Keep oldest as canonical.
		if r.CreationTimestamp.Before(&g.canonical.CreationTimestamp) {
			g.extras = append(g.extras, g.canonical)
			g.canonical = r
		} else {
			g.extras = append(g.extras, r)
		}
	}

	now := metav1.NewTime(c.now())
	for ns, g := range groups {
		if len(g.extras) == 0 {
			// Only one open Registry incident in this namespace — cache it and move on.
			c.openRegistryByNS[ns] = g.canonical.Name
			continue
		}

		// Merge AffectedResources from extras into canonical.
		canonicalBase := g.canonical.DeepCopy()
		for _, extra := range g.extras {
			for _, res := range extra.Status.AffectedResources {
				if !incidentAffectsPod(g.canonical, res.Name, res.Namespace) {
					g.canonical.Status.AffectedResources = append(g.canonical.Status.AffectedResources, res)
				}
			}
		}
		if err := c.client.Status().Patch(ctx, g.canonical, client.MergeFrom(canonicalBase)); err != nil {
			c.log.Error(err, "consolidate registry: failed to update canonical incident",
				"namespace", ns, "name", g.canonical.Name)
		}

		// Resolve duplicates.
		for _, extra := range g.extras {
			base := extra.DeepCopy()
			extra.Status.Phase = phaseResolved
			extra.Status.ResolvedTime = &now
			extra.Status.Timeline = append(extra.Status.Timeline, rcav1alpha1.TimelineEvent{
				Time:  now,
				Event: fmt.Sprintf("Merged into canonical incident %s during startup consolidation", g.canonical.Name),
			})
			extra.Status.Timeline = trimTimeline(extra.Status.Timeline)
			if err := c.client.Status().Patch(ctx, extra, client.MergeFrom(base)); err != nil {
				c.log.Error(err, "consolidate registry: failed to resolve duplicate incident",
					"namespace", ns, "name", extra.Name)
			} else {
				c.log.Info("Resolved duplicate Registry incident during startup consolidation",
					"namespace", ns,
					"resolved", extra.Name,
					"canonical", g.canonical.Name,
				)
			}
		}
		c.openRegistryByNS[ns] = g.canonical.Name
	}
	return nil
}

// reopenIncident transitions a Resolved IncidentReport back to Detecting,
// preserving its full history and appending a "re-opened" timeline entry.
// Used when a new watcher signal arrives within reopenWindow for the same
// pod and incident type, instead of creating a duplicate IncidentReport.
func (c *Consumer) reopenIncident(
	ctx context.Context,
	report *rcav1alpha1.IncidentReport,
	event watcher.CorrelatorEvent,
	podName string,
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
	report.Annotations[annotationLastSeen] = now.Format(time.RFC3339)
	report.Annotations[annotationSignalSeen] = incrementCounter(report.Annotations[annotationSignalSeen])
	report.Annotations[annotationSignal] = summary
	report.Annotations[annotationDedupKey] = event.DedupKey()
	if err := c.client.Patch(ctx, report, client.MergeFrom(metaBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport metadata on reopen: %w", err)
	}

	statusBase := report.DeepCopy()
	report.Status.Phase = phaseDetecting
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
	// Add pod to AffectedResources if not already tracked (namespace-scoped types).
	if podName != "" && !incidentAffectsPod(report, podName, report.Namespace) {
		report.Status.AffectedResources = append(report.Status.AffectedResources, rcav1alpha1.AffectedResource{
			Kind:      "Pod",
			Name:      podName,
			Namespace: report.Namespace,
		})
	}
	if err := c.client.Status().Patch(ctx, report, client.MergeFrom(statusBase)); err != nil {
		return fmt.Errorf("failed to patch IncidentReport status on reopen: %w", err)
	}

	c.log.Info("Reopened resolved IncidentReport",
		"namespace", report.Namespace,
		"name", report.Name,
		"eventType", event.Type(),
		"incidentType", report.Status.IncidentType,
	)
	// Refresh Registry dedup cache so subsequent events route to this incident.
	if report.Status.IncidentType == incidentTypeRegistry {
		c.openRegistryByNS[report.Namespace] = report.Name
	}
	return nil
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
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, "P1", fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return e.Namespace, e.PodName, e.AgentName, incidentTypeNodeFailure, "P2", fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return e.Namespace, e.PodName, e.AgentName, "ProbeFailure", "P3", fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		// The deployment name travels in both DeploymentName and BaseEvent.PodName
		// so the standard correlator routing path (findActiveIncidentForPodType,
		// label indexing, dedup key) works without a separate code path.
		return e.Namespace, e.DeploymentName, e.AgentName, "BadDeploy", "P2",
			fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
				e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
	case watcher.NodePressureEvent:
		// Node resource-pressure is a NodeFailure variant.  Severity varies by type:
		// DiskPressure and MemoryPressure threaten workload stability (P2);
		// PIDPressure is less common and treated as P3.
		severity := "P2"
		if e.PressureType == "PIDPressure" {
			severity = "P3"
		}
		// Node-level incident: NodeName doubles as the resource identifier (no pod).
		return e.Namespace, e.NodeName, e.AgentName, incidentTypeNodeFailure, severity,
			fmt.Sprintf("Node %s condition=%s message=%s", e.NodeName, e.PressureType, e.Message)
	case watcher.CPUThrottlingEvent:
		// CPU throttling indicates a container is hitting its cpu-limit; surfaced as
		// ResourceSaturation so it can be correlated with probe failures in Rule 6.
		return e.Namespace, e.PodName, e.AgentName, "ResourceSaturation", "P3",
			fmt.Sprintf("CPU throttling high container=%s message=%s", e.ContainerName, e.Message)
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
		if report.Status.Phase == phaseResolved {
			continue
		}
		if !incidentAffectsPod(report, event.PodName, event.Namespace) {
			continue
		}
		// Guard: skip incidents that received a watcher signal within signalCooldown.
		// Pods that briefly restart between failure cycles (OOMKilled, CrashLoop)
		// become Running+Ready for a few seconds, which would otherwise prematurely
		// resolve the incident and cause a new one to be created on the next cycle.
		// The reconciler's idle-window logic handles final resolution for these cases.
		if lastSeen := report.Annotations[annotationLastSeen]; lastSeen != "" {
			if t, err := time.Parse(time.RFC3339, lastSeen); err == nil {
				if c.now().Sub(t) < signalCooldown {
					continue
				}
			}
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
		if report.Status.Phase == phaseResolved {
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
	podName string,
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
