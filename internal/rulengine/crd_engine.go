// Package rulengine provides a generic, CRD-driven rule engine that loads
// RCACorrelationRule resources dynamically and evaluates them at runtime.
// Zero rules are hardcoded in Go — all rules are defined as Kubernetes CRDs.
package rulengine

import (
	"bytes"
	"context"
	"regexp"
	"sort"
	"strconv"
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

const (
	attributeOpEquals      = "Equals"
	attributeOpNotEquals   = "NotEquals"
	attributeOpContains    = "Contains"
	attributeOpNotContains = "NotContains"
	attributeOpRegex       = "Regex"
	attributeOpExists      = "Exists"
	attributeOpNotExists   = "NotExists"
	attributeOpGte         = "Gte"
	attributeOpLte         = "Lte"
	attributeOpGt          = "Gt"
	attributeOpLt          = "Lt"
)

// loadedRule is a compiled, ready-to-evaluate version of an RCACorrelationRule.
type loadedRule struct {
	name       string
	priority   int
	trigger    string
	conditions []loadedCondition
	fires      rcav1alpha1.RuleFires
	tmpl       *template.Template
}

type loadedCondition struct {
	eventType  string
	scope      string
	negate     bool
	attributes []compiledAttributeMatch
}

type compiledAttributeMatch struct {
	key    string
	op     string
	value  string
	regex  *regexp.Regexp
	number float64
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

// Buffer returns the underlying correlation buffer. This is used by the
// auto-detection subsystem to snapshot events for pattern mining.
func (e *CRDRuleEngine) Buffer() *correlator.Buffer {
	return e.buf
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
		conditions, err := compileConditions(rule.Spec.Conditions)
		if err != nil {
			e.log.Error(err, "Failed to compile correlation rule", "rule", rule.Name)
			continue
		}
		loaded = append(loaded, loadedRule{
			name:       rule.Name,
			priority:   rule.Spec.Priority,
			trigger:    rule.Spec.Trigger.EventType,
			conditions: conditions,
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
			Fired:      true,
			Severity:   rule.fires.Severity,
			Summary:    summary,
			Rule:       rule.name,
			Resource:   resource,
			ScopeLevel: rule.fires.Scope,
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

func (e *CRDRuleEngine) conditionsMet(trigger watcher.CorrelatorEvent, conditions []loadedCondition, entries []correlator.Entry) bool {
	for _, cond := range conditions {
		found := false
		for _, en := range entries {
			if string(en.Event.Type()) != cond.eventType {
				continue
			}
			if !e.scopeMatches(trigger, en.Event, cond.scope) {
				continue
			}
			if !attributesMatch(en.Event, cond.attributes) {
				continue
			}
			found = true
			break
		}
		if cond.negate {
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
	triggerBase := ExtractBase(trigger)
	candidateBase := ExtractBase(candidate)

	switch scope {
	case "samePod":
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.PodName == candidateBase.PodName && triggerBase.PodName != ""
	case "sameNode":
		return triggerBase.NodeName == candidateBase.NodeName && triggerBase.NodeName != ""
	case "sameNamespace":
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.Namespace != ""
	case "sameTrace":
		return sameTraceID(trigger, candidate)
	case "any":
		return true
	default:
		return triggerBase.Namespace == candidateBase.Namespace && triggerBase.PodName == candidateBase.PodName
	}
}

// sameTraceID returns true when both events expose a non-empty "trace.id" in
// their AttributesEvent map (or the promoted TraceID field surfaced via
// Attributes()). Events that do not implement AttributesEvent always compare
// unequal so rules using sameTrace can never cross the OTel ↔ K8s signal boundary.
func sameTraceID(a, b watcher.CorrelatorEvent) bool {
	aid := traceIDOf(a)
	bid := traceIDOf(b)
	return aid != "" && aid == bid
}

func traceIDOf(ev watcher.CorrelatorEvent) string {
	ae, ok := ev.(watcher.AttributesEvent)
	if !ok {
		return ""
	}
	attrs := ae.Attributes()
	if id, ok := attrs["trace.id"]; ok && id != "" {
		return id
	}
	// Fallback to direct struct field for span/log events (promoted via mergedAttrs
	// under the canonical "trace.id" key; this fallback covers legacy attributes).
	return attrs["trace_id"]
}

// attributesMatch evaluates every predicate in matches against an event. Events
// that do not implement watcher.AttributesEvent are treated as having no
// attributes — every predicate except NotExists fails for them, preserving
// backward compatibility with K8s-event-only rules that never set Attributes.
func attributesMatch(ev watcher.CorrelatorEvent, matches []compiledAttributeMatch) bool {
	if len(matches) == 0 {
		return true
	}
	var attrs map[string]string
	hasAttrs := false
	if ae, ok := ev.(watcher.AttributesEvent); ok {
		attrs = ae.Attributes()
		hasAttrs = true
	}
	for _, m := range matches {
		if !evaluateAttribute(attrs, hasAttrs, m) {
			return false
		}
	}
	return true
}

func evaluateAttribute(attrs map[string]string, hasAttrs bool, m compiledAttributeMatch) bool {
	if !hasAttrs {
		return m.op == attributeOpNotExists
	}
	val, present := attrs[m.key]
	switch m.op {
	case attributeOpEquals:
		return present && val == m.value
	case attributeOpNotEquals:
		return present && val != m.value
	case attributeOpContains:
		return present && strings.Contains(val, m.value)
	case attributeOpNotContains:
		return present && !strings.Contains(val, m.value)
	case attributeOpRegex:
		if !present || m.regex == nil {
			return false
		}
		return m.regex.MatchString(val)
	case attributeOpExists:
		return present && val != ""
	case attributeOpNotExists:
		return !present || val == ""
	case attributeOpGte, attributeOpLte, attributeOpGt, attributeOpLt:
		if !present {
			return false
		}
		lhs, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return false
		}
		switch m.op {
		case attributeOpGte:
			return lhs >= m.number
		case attributeOpLte:
			return lhs <= m.number
		case attributeOpGt:
			return lhs > m.number
		case attributeOpLt:
			return lhs < m.number
		}
	}
	return false
}

func compileConditions(conditions []rcav1alpha1.RuleCondition) ([]loadedCondition, error) {
	out := make([]loadedCondition, 0, len(conditions))
	for _, cond := range conditions {
		attributes, err := compileAttributeMatches(cond.Attributes)
		if err != nil {
			return nil, err
		}
		out = append(out, loadedCondition{
			eventType:  cond.EventType,
			scope:      cond.Scope,
			negate:     cond.Negate,
			attributes: attributes,
		})
	}
	return out, nil
}

func compileAttributeMatches(matches []rcav1alpha1.AttributeMatch) ([]compiledAttributeMatch, error) {
	out := make([]compiledAttributeMatch, 0, len(matches))
	for _, m := range matches {
		compiled := compiledAttributeMatch{
			key:   m.Key,
			op:    normalizeAttributeOp(m.Op),
			value: m.Value,
		}
		switch compiled.op {
		case attributeOpRegex:
			re, err := regexp.Compile(m.Value)
			if err != nil {
				return nil, err
			}
			compiled.regex = re
		case attributeOpGte, attributeOpLte, attributeOpGt, attributeOpLt:
			number, err := strconv.ParseFloat(m.Value, 64)
			if err != nil {
				return nil, err
			}
			compiled.number = number
		}
		out = append(out, compiled)
	}
	return out, nil
}

func normalizeAttributeOp(op string) string {
	if op == "" {
		return attributeOpEquals
	}
	return op
}

// ExtractBase extracts the BaseEvent fields from any CorrelatorEvent.
// Exported for use by the auto-detection pattern miner.
func ExtractBase(event watcher.CorrelatorEvent) watcher.BaseEvent {
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
	case watcher.StalledStatefulSetEvent:
		return e.BaseEvent
	case watcher.StalledDaemonSetEvent:
		return e.BaseEvent
	case watcher.JobFailedEvent:
		return e.BaseEvent
	case watcher.CronJobFailedEvent:
		return e.BaseEvent
	case watcher.OTelSpanErrorEvent:
		return e.BaseEvent
	case watcher.OTelSpanLatencySpikeEvent:
		return e.BaseEvent
	case watcher.OTelLogMatchEvent:
		return e.BaseEvent
	case watcher.OTelSpanEventEvent:
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
	base := ExtractBase(event)
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
	base := ExtractBase(event)
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
