package correlator

import (
	"fmt"
	"sort"
	"sync"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

type ruleFunc func(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult

type registeredRule struct {
	name     string
	priority int
	evaluate ruleFunc
}

func (r registeredRule) Name() string {
	return r.name
}

func (r registeredRule) Priority() int {
	return r.priority
}

func (r registeredRule) Evaluate(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	result := r.evaluate(event, entries)
	if result.Fired && result.Rule == "" {
		result.Rule = r.name
	}
	return result
}

var (
	ruleRegistryMu sync.RWMutex
	ruleRegistry   []Rule
)

// RegisterRule makes a rule discoverable by the rule engine at runtime.
func RegisterRule(rule Rule) {
	if rule == nil {
		panic("correlator: cannot register nil rule")
	}

	ruleRegistryMu.Lock()
	defer ruleRegistryMu.Unlock()

	for _, existing := range ruleRegistry {
		if existing.Name() == rule.Name() {
			panic(fmt.Sprintf("correlator: duplicate rule registration %q", rule.Name()))
		}
	}

	ruleRegistry = append(ruleRegistry, rule)
}

// RegisteredRules returns the ordered rule set discovered at runtime.
func RegisteredRules() []Rule {
	ruleRegistryMu.RLock()
	defer ruleRegistryMu.RUnlock()

	rules := append([]Rule(nil), ruleRegistry...)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority() == rules[j].Priority() {
			return rules[i].Name() < rules[j].Name()
		}
		return rules[i].Priority() > rules[j].Priority()
	})
	return rules
}

// No hardcoded rules are registered via init(). All correlation rules are
// loaded dynamically from RCACorrelationRule CRDs by the CRD rule engine.

// ─────────────────────────────────────────────────────────────────────────────
// Helpers retained for targeted tests and any future correlation that may need
// node affinity or pod dedup key extraction.
// ─────────────────────────────────────────────────────────────────────────────

// extractNodeForFailure returns the NodeName for pod-failure event types that
// carry node affinity information (CrashLoop, OOM, PodEvicted).
func extractNodeForFailure(e watcher.CorrelatorEvent) string {
	switch ev := e.(type) {
	case watcher.CrashLoopBackOffEvent:
		return ev.NodeName
	case watcher.OOMKilledEvent:
		return ev.NodeName
	case watcher.PodEvictedEvent:
		return ev.NodeName
	}
	return ""
}

// failurePodKey returns a "namespace/pod" string used to deduplicate pods.
func failurePodKey(e watcher.CorrelatorEvent) string {
	switch ev := e.(type) {
	case watcher.CrashLoopBackOffEvent:
		return ev.Namespace + "/" + ev.PodName
	case watcher.OOMKilledEvent:
		return ev.Namespace + "/" + ev.PodName
	case watcher.PodEvictedEvent:
		return ev.Namespace + "/" + ev.PodName
	}
	return ""
}
