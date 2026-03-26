package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/metrics"
)

const (
	SlackWebhookURLKey   = "webhookURL"
	PagerDutyAPIKeyKey   = "apiKey"
	pagerDutyEventsURL   = "https://events.pagerduty.com/v2/enqueue"
	severityP1           = "P1"
	severityP2           = "P2"
	slackChannelName     = "slack"
	pagerDutyChannelName = "pagerduty"
	resolveAction        = "resolve"
)

type Dispatcher struct {
	client     client.Client
	httpClient *http.Client
	log        logr.Logger
}

func NewDispatcher(c client.Client, logger logr.Logger) *Dispatcher {
	return &Dispatcher{
		client: c,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		log: logger.WithName("notifier"),
	}
}

func (d *Dispatcher) NotifyIncident(ctx context.Context, report *rcav1alpha1.IncidentReport, action string) error {
	if d == nil || report == nil {
		return nil
	}

	agent, err := d.findAgent(ctx, report)
	if err != nil {
		return err
	}
	if agent == nil || agent.Spec.Notifications == nil {
		return nil
	}

	if cfg := agent.Spec.Notifications.Slack; cfg != nil {
		if err := d.sendSlack(ctx, agent, report, cfg, action); err != nil {
			metrics.RecordNotification(slackChannelName, action, "error", report.Status.Severity)
			return err
		}
		metrics.RecordNotification(slackChannelName, action, "success", report.Status.Severity)
	}
	if cfg := agent.Spec.Notifications.PagerDuty; cfg != nil && shouldPage(cfg, report.Status.Severity) {
		if err := d.sendPagerDuty(ctx, agent, report, cfg, action); err != nil {
			metrics.RecordNotification(pagerDutyChannelName, action, "error", report.Status.Severity)
			return err
		}
		metrics.RecordNotification(pagerDutyChannelName, action, "success", report.Status.Severity)
	}

	return nil
}

func (d *Dispatcher) findAgent(ctx context.Context, report *rcav1alpha1.IncidentReport) (*rcav1alpha1.RCAAgent, error) {
	if report.Spec.AgentRef == "" {
		return nil, nil
	}

	agents := &rcav1alpha1.RCAAgentList{}
	if err := d.client.List(ctx, agents); err != nil {
		return nil, fmt.Errorf("list RCAAgents for notifications: %w", err)
	}

	var candidates []*rcav1alpha1.RCAAgent
	for i := range agents.Items {
		agent := &agents.Items[i]
		if agent.Name == report.Spec.AgentRef {
			candidates = append(candidates, agent)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	for _, agent := range candidates {
		if agent.Namespace == report.Namespace {
			return agent, nil
		}
		if len(agent.Spec.WatchNamespaces) == 0 || slices.Contains(agent.Spec.WatchNamespaces, report.Namespace) {
			return agent, nil
		}
	}

	return candidates[0], nil
}

func (d *Dispatcher) sendSlack(
	ctx context.Context,
	agent *rcav1alpha1.RCAAgent,
	report *rcav1alpha1.IncidentReport,
	cfg *rcav1alpha1.SlackConfig,
	action string,
) error {
	webhookURL, err := d.secretValue(ctx, agent.Namespace, cfg.WebhookSecretRef, SlackWebhookURLKey)
	if err != nil {
		return fmt.Errorf("load Slack webhook secret: %w", err)
	}

	payload := map[string]string{
		"text": slackMessage(report, cfg, action),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Slack payload: %w", err)
	}

	return d.postJSON(ctx, webhookURL, body, "Slack")
}

func (d *Dispatcher) sendPagerDuty(
	ctx context.Context,
	agent *rcav1alpha1.RCAAgent,
	report *rcav1alpha1.IncidentReport,
	cfg *rcav1alpha1.PagerDutyConfig,
	action string,
) error {
	routingKey, err := d.secretValue(ctx, agent.Namespace, cfg.SecretRef, PagerDutyAPIKeyKey)
	if err != nil {
		return fmt.Errorf("load PagerDuty secret: %w", err)
	}

	payload := map[string]any{
		"routing_key":  routingKey,
		"event_action": pagerDutyAction(action),
		"dedup_key":    report.Spec.Fingerprint,
	}
	if action != resolveAction {
		payload["payload"] = map[string]any{
			"summary":   notificationSummary(report),
			"source":    notificationSource(report),
			"severity":  pagerDutySeverity(report.Status.Severity),
			"component": report.Spec.IncidentType,
			"group":     report.Namespace,
			"class":     report.Status.Phase,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal PagerDuty payload: %w", err)
	}

	return d.postJSON(ctx, pagerDutyEventsURL, body, "PagerDuty")
}

func (d *Dispatcher) postJSON(ctx context.Context, url string, body []byte, service string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", service, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", service, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s returned %s: %s", service, resp.Status, strings.TrimSpace(string(data)))
	}

	return nil
}

func (d *Dispatcher) secretValue(ctx context.Context, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := d.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return "", err
	}
	value, ok := secret.Data[key]
	if !ok || len(value) == 0 {
		return "", fmt.Errorf("secret %s/%s missing key %q", namespace, name, key)
	}
	return strings.TrimSpace(string(value)), nil
}

func slackMessage(report *rcav1alpha1.IncidentReport, cfg *rcav1alpha1.SlackConfig, action string) string {
	state := "TRIGGERED"
	if action == resolveAction {
		state = "RESOLVED"
	}
	mention := ""
	if action != resolveAction && report.Status.Severity == severityP1 && cfg.MentionOnP1 != "" {
		mention = cfg.MentionOnP1 + " "
	}
	return fmt.Sprintf(
		"%s[%s] %s %s in %s for %s\n%s",
		mention,
		report.Status.Severity,
		state,
		report.Spec.IncidentType,
		report.Namespace,
		report.Spec.AgentRef,
		notificationSummary(report),
	)
}

func notificationSummary(report *rcav1alpha1.IncidentReport) string {
	if report.Status.Summary != "" {
		return report.Status.Summary
	}
	if len(report.Status.Timeline) > 0 {
		return report.Status.Timeline[len(report.Status.Timeline)-1].Event
	}
	return fmt.Sprintf("%s incident in %s", report.Spec.IncidentType, report.Namespace)
}

func notificationSource(report *rcav1alpha1.IncidentReport) string {
	if report.Spec.Scope.ResourceRef != nil && report.Spec.Scope.ResourceRef.Name != "" {
		if report.Spec.Scope.ResourceRef.Namespace != "" {
			return report.Spec.Scope.ResourceRef.Namespace + "/" + report.Spec.Scope.ResourceRef.Name
		}
		return report.Spec.Scope.ResourceRef.Name
	}
	if start := incidentstatus.EffectiveStartTime(report.Status); start != nil {
		return fmt.Sprintf("%s/%s@%s", report.Namespace, report.Name, start.Format(time.RFC3339))
	}
	return report.Namespace + "/" + report.Name
}

func shouldPage(cfg *rcav1alpha1.PagerDutyConfig, severity string) bool {
	if cfg == nil {
		return false
	}
	threshold := cfg.Severity
	if threshold == "" {
		threshold = severityP2
	}
	rank := map[string]int{severityP1: 4, severityP2: 3, "P3": 2, "P4": 1}
	return rank[severity] >= rank[threshold]
}

func pagerDutyAction(action string) string {
	if action == resolveAction {
		return "resolve"
	}
	return "trigger"
}

func pagerDutySeverity(severity string) string {
	switch severity {
	case severityP1:
		return "critical"
	case severityP2:
		return "error"
	case "P3":
		return "warning"
	default:
		return "info"
	}
}
