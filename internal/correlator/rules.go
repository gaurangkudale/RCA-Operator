package correlator

import (
	"fmt"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// ruleFunc is the signature for every correlation rule.
// event is the newly-arrived event; entries is a snapshot of the current buffer
// (which already includes event). Return a fired CorrelationResult to override
// the default single-event classification.
type ruleFunc func(event watcher.CorrelatorEvent, entries []entry) CorrelationResult

// allRules is the ordered list of correlation rules evaluated by Correlator.Evaluate.
// Higher-severity rules are placed first so that a P1 result is not shadowed by a
// later P2 rule when both could fire for the same event.
var allRules = []ruleFunc{
	ruleNodeNotReadyPlusEviction, // Rule 5 — P1
	ruleCrashLoopPlusOOM,         // Rule 1 — P2
	ruleMultiPodNodeFailure,      // Rule 3 — P2
	ruleCrashLoopPlusBadDeploy,   // Rule 2 — P2
	ruleImagePullNoHistory,       // Rule 4 — P2 (escalated from P3)
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 1: CrashLoop + OOMKilled → MemoryPressure (type=OOM, severity=P2)
// ─────────────────────────────────────────────────────────────────────────────

func ruleCrashLoopPlusOOM(event watcher.CorrelatorEvent, entries []entry) CorrelationResult {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		for _, en := range entries {
			oom, ok := en.event.(watcher.OOMKilledEvent)
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
			cl, ok := en.event.(watcher.CrashLoopBackOffEvent)
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

func ruleCrashLoopPlusBadDeploy(event watcher.CorrelatorEvent, entries []entry) CorrelationResult {
	cl, ok := event.(watcher.CrashLoopBackOffEvent)
	if !ok {
		return CorrelationResult{}
	}
	for _, en := range entries {
		stalled, ok := en.event.(watcher.StalledRolloutEvent)
		if ok && stalled.Namespace == cl.Namespace {
			return CorrelationResult{
				Fired:        true,
				IncidentType: "BadDeploy",
				Severity:     "P2",
				Summary:      fmt.Sprintf("BadDeploy: CrashLoop after stalled rollout deployment=%s pod=%s", stalled.DeploymentName, cl.PodName),
				Rule:         "CrashLoopPlusBadDeploy",
			}
		}
	}
	return CorrelationResult{}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 3: Multiple pods failing on same node → NodeFailure, P2
// ─────────────────────────────────────────────────────────────────────────────

func ruleMultiPodNodeFailure(event watcher.CorrelatorEvent, entries []entry) CorrelationResult {
	nodeName := extractNodeForFailure(event)
	if nodeName == "" {
		return CorrelationResult{}
	}

	// Collect distinct failing pods (including the current event) on this node.
	failedPods := map[string]struct{}{}
	for _, en := range entries {
		n := extractNodeForFailure(en.event)
		if n != nodeName {
			continue
		}
		if k := failurePodKey(en.event); k != "" {
			failedPods[k] = struct{}{}
		}
	}

	if len(failedPods) >= 2 {
		return CorrelationResult{
			Fired:        true,
			IncidentType: "NodeFailure",
			Severity:     "P2",
			Summary:      fmt.Sprintf("NodeLevel: %d pods failing on node=%s", len(failedPods), nodeName),
			Rule:         "MultiPodNodeFailure",
		}
	}
	return CorrelationResult{}
}

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
func ruleImagePullNoHistory(event watcher.CorrelatorEvent, entries []entry) CorrelationResult {
	pull, ok := event.(watcher.ImagePullBackOffEvent)
	if !ok {
		return CorrelationResult{}
	}
	for _, en := range entries {
		healthy, ok := en.event.(watcher.PodHealthyEvent)
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

func ruleNodeNotReadyPlusEviction(event watcher.CorrelatorEvent, entries []entry) CorrelationResult {
	switch e := event.(type) {
	case watcher.NodeNotReadyEvent:
		for _, en := range entries {
			evicted, ok := en.event.(watcher.PodEvictedEvent)
			if ok && evicted.NodeName == e.NodeName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "NodeFailure",
					Severity:     "P1",
					Summary:      fmt.Sprintf("NodeFailure: NodeNotReady+Eviction node=%s reason=%s evictedPod=%s", e.NodeName, e.Reason, evicted.PodName),
					Rule:         "NodeNotReadyPlusEviction",
				}
			}
		}
	case watcher.PodEvictedEvent:
		for _, en := range entries {
			notReady, ok := en.event.(watcher.NodeNotReadyEvent)
			if ok && notReady.NodeName == e.NodeName {
				return CorrelationResult{
					Fired:        true,
					IncidentType: "NodeFailure",
					Severity:     "P1",
					Summary:      fmt.Sprintf("NodeFailure: NodeNotReady+Eviction node=%s reason=%s evictedPod=%s", notReady.NodeName, notReady.Reason, e.PodName),
					Rule:         "NodeNotReadyPlusEviction",
				}
			}
		}
	}
	return CorrelationResult{}
}
