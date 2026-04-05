package autodetect

import (
	"sort"
	"strings"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/rulengine"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const (
	// Scope constants for co-occurrence patterns.
	ScopeSamePod       = "samePod"
	ScopeSameNode      = "sameNode"
	ScopeSameNamespace = "sameNamespace"
)

// EventPair represents a co-occurring event pair extracted from a buffer
// snapshot. TriggerType is lexicographically ≤ ConditionType (canonical order).
type EventPair struct {
	TriggerType   string // e.g. "CrashLoopBackOff"
	ConditionType string // e.g. "OOMKilled"
	Scope         string // "samePod" | "sameNode" | "sameNamespace"
}

// NormalizePair canonicalizes an EventPair by ensuring lexicographic order
// for trigger/condition. Scope is preserved.
func NormalizePair(pair EventPair) EventPair {
	if pair.ConditionType < pair.TriggerType {
		pair.TriggerType, pair.ConditionType = pair.ConditionType, pair.TriggerType
	}
	return pair
}

// Key returns a dedup key for this pattern.
func (p EventPair) Key() string {
	return p.TriggerType + ":" + p.ConditionType + ":" + p.Scope
}

// MineResult holds the mined pairs from a buffer snapshot.
type MineResult struct {
	Pairs []EventPair
}

// baseEntry holds extracted event info for pattern mining.
type baseEntry struct {
	base      watcher.BaseEvent
	eventType string
	index     int // position in entries for temporal ordering
}

// nodeEventTypes lists event types that originate at the node level.
// These should only correlate with other node events at sameNode scope,
// never with pod-level events (which would create misleading rules).
var nodeEventTypes = map[string]bool{
	string(watcher.EventTypeNodeNotReady): true,
	string(watcher.EventTypeNodePressure): true,
	string(watcher.EventTypePodEvicted):   true,
}

// workloadEventTypes lists event types that originate at the workload controller
// level. These correlate at sameNamespace scope.
var workloadEventTypes = map[string]bool{
	string(watcher.EventTypeStalledRollout):     true,
	string(watcher.EventTypeStalledStatefulSet): true,
	string(watcher.EventTypeStalledDaemonSet):   true,
	string(watcher.EventTypeJobFailed):          true,
	string(watcher.EventTypeCronJobFailed):      true,
}

// IsNodeEventType returns true when the event type is a node-level signal.
func IsNodeEventType(et string) bool {
	return nodeEventTypes[et]
}

// IsWorkloadEventType returns true when the event type is a workload-level signal.
func IsWorkloadEventType(et string) bool {
	return workloadEventTypes[et]
}

// IsPodEventType returns true when the event type is a pod-level signal.
func IsPodEventType(et string) bool {
	return !IsNodeEventType(et) && !IsWorkloadEventType(et)
}

// IsValidScopePair validates that pair scope aligns with event-type level.
func IsValidScopePair(pair EventPair) bool {
	switch pair.Scope {
	case ScopeSamePod:
		return IsPodEventType(pair.TriggerType) && IsPodEventType(pair.ConditionType)
	case ScopeSameNode:
		return IsNodeEventType(pair.TriggerType) && IsNodeEventType(pair.ConditionType)
	case ScopeSameNamespace:
		return IsWorkloadEventType(pair.TriggerType) && IsWorkloadEventType(pair.ConditionType)
	default:
		return false
	}
}

// isPodEvent returns true for pod-scoped events (not node or workload level).
func isPodEvent(et string) bool {
	return IsPodEventType(et)
}

// MinePatterns extracts co-occurring event pairs from a buffer snapshot.
//
// Scope rules enforce clean signal boundaries:
//   - samePod:       only pod-level events (CrashLoop, OOM, ImagePull, Probe, etc.)
//   - sameNode:      only node-level events (NodeNotReady, NodePressure, PodEvicted)
//   - sameNamespace: only workload-level events (StalledRollout, JobFailed, etc.)
//
// This prevents noisy cross-scope rules like "CrashLoopBackOff + NodeNotReady on sameNode".
// Lifecycle events (PodHealthy, PodDeleted) are always skipped.
func MinePatterns(entries []correlator.Entry) MineResult {

	// Extract base info and filter lifecycle events.
	var filtered []baseEntry
	for i, entry := range entries {
		et := string(entry.Event.Type())
		if et == string(watcher.EventTypePodHealthy) || et == string(watcher.EventTypePodDeleted) {
			continue
		}
		base := rulengine.ExtractBase(entry.Event)
		filtered = append(filtered, baseEntry{base: base, eventType: et, index: i})
	}

	seen := make(map[string]bool)
	var pairs []EventPair

	// ── samePod: only pod-level events ──────────────────────────────────
	podGroups := make(map[string][]baseEntry)
	for _, e := range filtered {
		if isPodEvent(e.eventType) && e.base.Namespace != "" && e.base.PodName != "" {
			key := e.base.Namespace + ":" + e.base.PodName
			podGroups[key] = append(podGroups[key], e)
		}
	}
	emitPairs(podGroups, ScopeSamePod, seen, &pairs)

	// ── sameNode: only node-level events ────────────────────────────────
	nodeGroups := make(map[string][]baseEntry)
	for _, e := range filtered {
		if nodeEventTypes[e.eventType] && e.base.NodeName != "" {
			nodeGroups[e.base.NodeName] = append(nodeGroups[e.base.NodeName], e)
		}
	}
	emitPairs(nodeGroups, ScopeSameNode, seen, &pairs)

	// ── sameNamespace: only workload-level events ───────────────────────
	nsGroups := make(map[string][]baseEntry)
	for _, e := range filtered {
		if workloadEventTypes[e.eventType] && e.base.Namespace != "" {
			nsGroups[e.base.Namespace] = append(nsGroups[e.base.Namespace], e)
		}
	}
	emitPairs(nsGroups, ScopeSameNamespace, seen, &pairs)

	return MineResult{Pairs: pairs}
}

// emitPairs generates canonically-ordered event pairs within each group.
// Pairs are sorted lexicographically (TriggerType < ConditionType) to prevent
// mirror-image duplicates. The seen map prevents the same pair from being
// emitted more than once across groups.
func emitPairs(groups map[string][]baseEntry, scope string, seen map[string]bool, out *[]EventPair) {
	for _, group := range groups {
		// Collect distinct event types in this group.
		typeSet := make(map[string]bool)
		for _, e := range group {
			typeSet[e.eventType] = true
		}
		if len(typeSet) < 2 {
			continue
		}

		// Sort so pairs are emitted in canonical order.
		types := make([]string, 0, len(typeSet))
		for t := range typeSet {
			types = append(types, t)
		}
		sort.Strings(types)

		for i := 0; i < len(types); i++ {
			for j := i + 1; j < len(types); j++ {
				pair := EventPair{
					TriggerType:   types[i],
					ConditionType: types[j],
					Scope:         scope,
				}
				pairKey := pair.Key()
				if seen[pairKey] {
					continue
				}
				seen[pairKey] = true
				*out = append(*out, pair)
			}
		}
	}
}

// RuleName generates a Kubernetes-safe name for an auto-generated rule.
// Format: auto-<trigger>-<condition>-<scope>, lowercased, max 63 chars.
func RuleName(pair EventPair) string {
	name := "auto-" + strings.ToLower(pair.TriggerType) + "-" + strings.ToLower(pair.ConditionType) + "-" + strings.ToLower(pair.Scope)
	// Kubernetes names must be lowercase alphanumeric + hyphens, max 63 chars.
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 63 {
		name = name[:63]
	}
	// Trim trailing hyphens.
	name = strings.TrimRight(name, "-")
	return name
}
