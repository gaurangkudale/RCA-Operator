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

func init() {
	RegisterRule(registeredRule{name: "NodeNotReadyPlusEviction", priority: 500, evaluate: ruleNodeNotReadyPlusEviction})
	RegisterRule(registeredRule{name: "CrashLoopPlusOOM", priority: 400, evaluate: ruleCrashLoopPlusOOM})
	RegisterRule(registeredRule{name: "CrashLoopPlusBadDeploy", priority: 300, evaluate: ruleCrashLoopPlusBadDeploy})
	RegisterRule(registeredRule{name: "ImagePullNoHistory", priority: 200, evaluate: ruleImagePullNoHistory})
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 1: CrashLoop + OOMKilled → MemoryPressure (type=OOM, severity=P2)
// ─────────────────────────────────────────────────────────────────────────────

func ruleCrashLoopPlusOOM(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		for _, en := range entries {
			oom, ok := en.Event.(watcher.OOMKilledEvent)
			if ok && oom.Namespace == e.Namespace && oom.PodName == e.PodName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "OOM",
					Severity:     "P2",
					Summary:      fmt.Sprintf("MemoryPressure: CrashLoop+OOMKilled pod=%s container=%s restarts=%d", e.PodName, e.ContainerName, e.RestartCount),
					Rule:         "CrashLoopPlusOOM",
				}
			}
		}
	case watcher.OOMKilledEvent:
		for _, en := range entries {
			cl, ok := en.Event.(watcher.CrashLoopBackOffEvent)
			if ok && cl.Namespace == e.Namespace && cl.PodName == e.PodName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "OOM",
					Severity:     "P2",
					Summary:      fmt.Sprintf("MemoryPressure: CrashLoop+OOMKilled pod=%s container=%s restarts=%d", e.PodName, e.ContainerName, cl.RestartCount),
					Rule:         "CrashLoopPlusOOM",
				}
			}
		}
	}
	return CorrelationResult{}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 2: CrashLoop + recent StalledRollout in same namespace → BadDeploy, P2
// ─────────────────────────────────────────────────────────────────────────────

func ruleCrashLoopPlusBadDeploy(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	cl, ok := event.(watcher.CrashLoopBackOffEvent)
	if !ok {
		return CorrelationResult{}
	}
	for _, en := range entries {
		stalled, ok := en.Event.(watcher.StalledRolloutEvent)
		if ok && stalled.Namespace == cl.Namespace {
			return CorrelationResult{
				Fired:        true,
				IncidentType: "BadDeploy",
				Severity:     "P2",
				Summary:      fmt.Sprintf("BadDeploy: CrashLoop after stalled rollout deployment=%s pod=%s", stalled.DeploymentName, cl.PodName),
				Rule:         "CrashLoopPlusBadDeploy",
				// Use deployment name so the incident deduplicates with the
				// BadDeploy incident created from the StalledRollout signal.
				Resource: stalled.DeploymentName,
			}
		}
	}
	return CorrelationResult{}
}

// extractNodeForFailure returns the NodeName for pod-failure event types that
// carry node affinity information (CrashLoop, OOM, PodEvicted). These helpers
// are retained for targeted tests and future correlation that may need node
// affinity without mapping pod failures directly into NodeFailure incidents.
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

// failurePodKey returns a "namespace/pod" string used to deduplicate pods in rule 3.
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

// ─────────────────────────────────────────────────────────────────────────────
// Rule 4: ImagePullBackOff + no prior PodHealthy in window → Registry, P2
// ─────────────────────────────────────────────────────────────────────────────

// ruleImagePullNoHistory escalates an ImagePullBackOff from P3 to P2 when the
// buffer contains no PodHealthyEvent for the same pod, indicating the container
// image has never successfully started in the current window.
func ruleImagePullNoHistory(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	pull, ok := event.(watcher.ImagePullBackOffEvent)
	if !ok {
		return CorrelationResult{}
	}
	for _, en := range entries {
		healthy, ok := en.Event.(watcher.PodHealthyEvent)
		if ok && healthy.Namespace == pull.Namespace && healthy.PodName == pull.PodName {
			// Pod was healthy recently — treat as transient pull failure, do not escalate.
			return CorrelationResult{}
		}
	}
	return CorrelationResult{
		Fired:        true,
		IncidentType: "Registry",
		Severity:     "P2",
		Summary:      fmt.Sprintf("Registry: ImagePullBackOff with no prior success pod=%s container=%s reason=%s", pull.PodName, pull.ContainerName, pull.Reason),
		Rule:         "ImagePullNoHistory",
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 5: NodeNotReady + eviction events on same node → NodeFailure, P1
// ─────────────────────────────────────────────────────────────────────────────

func ruleNodeNotReadyPlusEviction(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	switch e := event.(type) {
	case watcher.NodeNotReadyEvent:
		for _, en := range entries {
			evicted, ok := en.Event.(watcher.PodEvictedEvent)
			if ok && evicted.NodeName == e.NodeName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "NodeFailure",
					Severity:     "P1",
					Summary:      fmt.Sprintf("NodeFailure: NodeNotReady+Eviction node=%s reason=%s evictedPod=%s", e.NodeName, e.Reason, evicted.PodName),
					Rule:         "NodeNotReadyPlusEviction",
					// Use node name so all signals from the same failed node
					// dedup into one NodeFailure incident.
					Resource: e.NodeName,
				}
			}
		}
	case watcher.PodEvictedEvent:
		for _, en := range entries {
			notReady, ok := en.Event.(watcher.NodeNotReadyEvent)
			if ok && notReady.NodeName == e.NodeName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "NodeFailure",
					Severity:     "P1",
					Summary:      fmt.Sprintf("NodeFailure: NodeNotReady+Eviction node=%s reason=%s evictedPod=%s", notReady.NodeName, notReady.Reason, e.PodName),
					Rule:         "NodeNotReadyPlusEviction",
					Resource:     notReady.NodeName,
				}
			}
		}
	}
	return CorrelationResult{}
}
