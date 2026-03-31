// Package rulengine provides a generic, CRD-driven rule engine that loads
// RCACorrelationRule resources dynamically and evaluates them at runtime.
// Zero rules are hardcoded in Go — all rules are defined as Kubernetes CRDs.
package rulengine

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// loadedRule is a compiled, ready-to-evaluate version of an RCACorrelationRule.
type loadedRule struct {
	name       string
	priority   int
	trigger    string
	conditions []rcav1alpha1.RuleCondition
	fires      rcav1alpha1.RuleFires
	tmpl       *template.Template
}

// CRDRuleEngine loads RCACorrelationRule CRDs from the cluster and evaluates
// them against incoming events. It satisfies the correlator.RuleEngine interface.
type CRDRuleEngine struct {
	client client.Client
	buf    *correlator.Buffer
	rules  []loadedRule
	mu     sync.RWMutex
	log    logr.Logger
}

// NewCRDRuleEngine creates a new CRD-driven rule engine.
func NewCRDRuleEngine(c client.Client, window time.Duration, logger logr.Logger) *CRDRuleEngine {
	return &CRDRuleEngine{
		client: c,
		buf:    correlator.NewBuffer(window),
		log:    logger.WithName("crd-rule-engine"),
	}
}

// Name returns the engine identifier for the factory registry.
func (e *CRDRuleEngine) Name() string {
	return "crd"
}

// LoadRules fetches all RCACorrelationRule CRDs from the cluster and compiles them.
func (e *CRDRuleEngine) LoadRules(ctx context.Context) error {
	list := &rcav1alpha1.RCACorrelationRuleList{}
	if err := e.client.List(ctx, list); err != nil {
		return err
	}

	loaded := make([]loadedRule, 0, len(list.Items))
	for i := range list.Items {
		rule := &list.Items[i]
		tmpl, err := template.New(rule.Name).Parse(rule.Spec.Fires.Summary)
		if err != nil {
			e.log.Error(err, "Failed to parse rule summary template", "rule", rule.Name)
			continue
		}
		loaded = append(loaded, loadedRule{
			name:       rule.Name,
			priority:   rule.Spec.Priority,
			trigger:    rule.Spec.Trigger.EventType,
			conditions: rule.Spec.Conditions,
			fires:      rule.Spec.Fires,
			tmpl:       tmpl,
		})
	}

	sort.SliceStable(loaded, func(i, j int) bool {
		if loaded[i].priority == loaded[j].priority {
			return loaded[i].name < loaded[j].name
		}
		return loaded[i].priority > loaded[j].priority
	})

	e.mu.Lock()
	e.rules = loaded
	e.mu.Unlock()

	e.log.Info("Loaded correlation rules from CRDs", "count", len(loaded))
	return nil
}

// Add records an event in the sliding-window buffer.
func (e *CRDRuleEngine) Add(event watcher.CorrelatorEvent) {
	e.buf.Add(event)
}

// Evaluate runs all loaded CRD rules against the incoming event and the current
// buffer contents. The first rule whose conditions match wins.
func (e *CRDRuleEngine) Evaluate(event watcher.CorrelatorEvent) correlator.CorrelationResult {
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	entries := e.buf.Snapshot()
	eventType := string(event.Type())

	for _, rule := range rules {
		if rule.trigger != eventType {
			continue
		}
		if !e.conditionsMet(event, rule.conditions, entries) {
			continue
		}

		summary := e.renderSummary(rule.tmpl, event)
		resource := e.resolveResource(rule.fires, event)

		return correlator.CorrelationResult{
			Fired:        true,
			IncidentType: rule.fires.IncidentType,
			Severity:     rule.fires.Severity,
			Summary:      summary,
			Rule:         rule.name,
			Resource:     resource,
		}
	}

	return correlator.CorrelationResult{}
}

// RuleCount returns the number of loaded rules.
func (e *CRDRuleEngine) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

func (e *CRDRuleEngine) conditionsMet(trigger watcher.CorrelatorEvent, conditions []rcav1alpha1.RuleCondition, entries []correlator.Entry) bool {
	for _, cond := range conditions {
		found := false
		for _, en := range entries {
			if string(en.Event.Type()) != cond.EventType {
				continue
			}
			if e.scopeMatches(trigger, en.Event, cond.Scope) {
				found = true
				break
			}
		}
		if cond.Negate {
			if found {
				return false
			}
		} else {
			if !found {
				return false
			}
		}
	}
	return true
}

func (e *CRDRuleEngine) scopeMatches(trigger, candidate watcher.CorrelatorEvent, scope string) bool {
	triggerBase := extractBase(trigger)
	candidateBase := extractBase(candidate)

	switch scope {
	case "samePod":
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.PodName == candidateBase.PodName && triggerBase.PodName != ""
	case "sameNode":
		return triggerBase.NodeName == candidateBase.NodeName && triggerBase.NodeName != ""
	case "sameNamespace":
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.Namespace != ""
	case "any":
		return true
	default:
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.PodName == candidateBase.PodName
	}
}

func extractBase(event watcher.CorrelatorEvent) watcher.BaseEvent {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.BaseEvent
	case watcher.OOMKilledEvent:
		return e.BaseEvent
	case watcher.ImagePullBackOffEvent:
		return e.BaseEvent
	case watcher.PodPendingTooLongEvent:
		return e.BaseEvent
	case watcher.GracePeriodViolationEvent:
		return e.BaseEvent
	case watcher.NodeNotReadyEvent:
		return e.BaseEvent
	case watcher.PodEvictedEvent:
		return e.BaseEvent
	case watcher.ProbeFailureEvent:
		return e.BaseEvent
	case watcher.StalledRolloutEvent:
		return e.BaseEvent
	case watcher.NodePressureEvent:
		return e.BaseEvent
	default:
		return watcher.BaseEvent{}
	}
}

type templateContext struct {
	PodName   string
	Namespace string
	NodeName  string
	EventType string
}

func (e *CRDRuleEngine) renderSummary(tmpl *template.Template, event watcher.CorrelatorEvent) string {
	base := extractBase(event)
	ctx := templateContext{
		PodName:   base.PodName,
		Namespace: base.Namespace,
		NodeName:  base.NodeName,
		EventType: string(event.Type()),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return tmpl.Name() + ": template render error"
	}
	return buf.String()
}

func (e *CRDRuleEngine) resolveResource(fires rcav1alpha1.RuleFires, event watcher.CorrelatorEvent) string {
	base := extractBase(event)
	switch strings.ToLower(fires.Resource) {
	case "node":
		return base.NodeName
	case "deployment":
		if sr, ok := event.(watcher.StalledRolloutEvent); ok {
			return sr.DeploymentName
		}
		return base.PodName
	default:
		return ""
	}
}
