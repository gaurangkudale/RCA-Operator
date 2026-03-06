package correlator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const defaultDedupCooldown = 2 * time.Minute

// Consumer reads watcher events, performs deduplication, and writes IncidentReport CRs.
type Consumer struct {
	client   client.Client
	events   <-chan watcher.CorrelatorEvent
	log      logr.Logger
	cooldown time.Duration
	now      func() time.Time

	mu        sync.Mutex
	lastFired map[string]time.Time
}

// NewConsumer returns a correlator consumer with a sensible default dedup cooldown.
func NewConsumer(c client.Client, events <-chan watcher.CorrelatorEvent, logger logr.Logger) *Consumer {
	return &Consumer{
		client:    c,
		events:    events,
		log:       logger.WithName("correlator-consumer"),
		cooldown:  defaultDedupCooldown,
		now:       time.Now,
		lastFired: make(map[string]time.Time),
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
			if !c.shouldProcess(event) {
				continue
			}
			if err := c.handleEvent(ctx, event); err != nil {
				c.log.Error(err, "Could not process watcher event", "eventType", event.Type(), "dedupKey", event.DedupKey())
			}
		}
	}
}

func (c *Consumer) shouldProcess(event watcher.CorrelatorEvent) bool {
	now := c.now()
	key := event.DedupKey()

	c.mu.Lock()
	defer c.mu.Unlock()

	if at, ok := c.lastFired[key]; ok && now.Sub(at) < c.cooldown {
		return false
	}
	c.lastFired[key] = now
	return true
}

func (c *Consumer) handleEvent(ctx context.Context, event watcher.CorrelatorEvent) error {
	namespace, podName, agentRef, incidentType, severity, summary := mapEvent(event)
	if namespace == "" || podName == "" {
		return nil
	}

	hasRecent, err := c.hasRecentIncidentReport(ctx, namespace, event.DedupKey())
	if err != nil {
		return err
	}
	if hasRecent {
		return nil
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
				"rca.rca-operator.io/agent":         agentRef,
				"rca.rca-operator.io/severity":      severity,
				"rca.rca-operator.io/incident-type": incidentType,
			},
			Annotations: map[string]string{
				"rca.rca-operator.io/signal":    summary,
				"rca.rca-operator.io/dedup-key": event.DedupKey(),
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
		Phase:        "Active",
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

func (c *Consumer) hasRecentIncidentReport(ctx context.Context, namespace, dedupKey string) (bool, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := c.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("failed to list IncidentReports: %w", err)
	}

	now := c.now()
	for i := range list.Items {
		report := &list.Items[i]
		if report.Annotations == nil {
			continue
		}
		if report.Annotations["rca.rca-operator.io/dedup-key"] != dedupKey {
			continue
		}
		if now.Sub(report.CreationTimestamp.Time) >= c.cooldown {
			continue
		}
		if report.Status.Phase == "Resolved" {
			continue
		}
		return true, nil
	}

	return false, nil
}

func mapEvent(event watcher.CorrelatorEvent) (namespace, podName, agentRef, incidentType, severity, summary string) {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.Namespace, e.PodName, e.AgentName, "CrashLoop", "P3", fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
	case watcher.OOMKilledEvent:
		return e.Namespace, e.PodName, e.AgentName, "OOM", "P2", fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
	case watcher.ImagePullBackOffEvent:
		return e.Namespace, e.PodName, e.AgentName, "Registry", "P3", fmt.Sprintf("Image pull failure reason=%s", e.Reason)
	case watcher.PodPendingTooLongEvent:
		// Pending can be caused by scheduling/capacity/image/constraints; treat as bad deployment signal for now.
		return e.Namespace, e.PodName, e.AgentName, "BadDeploy", "P3", fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	default:
		return "", "", "", "", "", ""
	}
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
