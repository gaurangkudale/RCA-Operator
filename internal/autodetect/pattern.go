package autodetect

import (
	"strings"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/rulengine"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// EventPair represents a co-occurring event pair extracted from a buffer
// snapshot. TriggerType precedes or equals ConditionType temporally.
type EventPair struct {
	TriggerType   string // e.g. "CrashLoopBackOff"
	ConditionType string // e.g. "OOMKilled"
	Scope         string // "samePod" | "sameNode" | "sameNamespace"
}

// Key returns a dedup key for this pattern.
func (p EventPair) Key() string {
	return p.TriggerType + ":" + p.ConditionType + ":" + p.Scope
}

// MineResult holds the mined pairs and per-event-type trigger counts
// for confidence calculation.
type MineResult struct {
	Pairs         []EventPair
	TriggerCounts map[string]int // event type → count of occurrences
}

// baseEntry holds extracted event info for pattern mining.
type baseEntry struct {
	base      watcher.BaseEvent
	eventType string
	index     int // position in entries for temporal ordering
}

// MinePatterns extracts co-occurring event pairs from a buffer snapshot.
// It groups entries by shared scope dimensions and emits ordered pairs
// where the trigger precedes or equals the condition temporally.
// Lifecycle events (PodHealthy, PodDeleted) are skipped.
func MinePatterns(entries []correlator.Entry) MineResult {

	// Extract base info and filter lifecycle events.
	var filtered []baseEntry
	triggerCounts := make(map[string]int)
	for i, entry := range entries {
		et := string(entry.Event.Type())
		if et == string(watcher.EventTypePodHealthy) || et == string(watcher.EventTypePodDeleted) {
			continue
		}
		base := rulengine.ExtractBase(entry.Event)
		filtered = append(filtered, baseEntry{base: base, eventType: et, index: i})
		triggerCounts[et]++
	}

	// Track seen pairs to deduplicate within a single tick.
	seen := make(map[string]bool)
	var pairs []EventPair

	// Group by samePod (tightest scope).
	podGroups := make(map[string][]baseEntry) // "ns:pod" → entries
	for _, e := range filtered {
		if e.base.Namespace != "" && e.base.PodName != "" {
			key := e.base.Namespace + ":" + e.base.PodName
			podGroups[key] = append(podGroups[key], e)
		}
	}
	podPairKeys := emitPairs(podGroups, "samePod", seen, &pairs)

	// Group by sameNode (medium scope). Skip pairs already found at samePod.
	nodeGroups := make(map[string][]baseEntry)
	for _, e := range filtered {
		if e.base.NodeName != "" {
			nodeGroups[e.base.NodeName] = append(nodeGroups[e.base.NodeName], e)
		}
	}
	nodePairKeys := emitPairs(nodeGroups, "sameNode", seen, &pairs)
	// Merge for namespace filtering.
	for k := range podPairKeys {
		nodePairKeys[k] = true
	}

	// Group by sameNamespace (widest scope). Skip pairs already found at tighter scopes.
	nsGroups := make(map[string][]baseEntry)
	for _, e := range filtered {
		if e.base.Namespace != "" {
			nsGroups[e.base.Namespace] = append(nsGroups[e.base.Namespace], e)
		}
	}
	emitPairs(nsGroups, "sameNamespace", seen, &pairs)

	return MineResult{Pairs: pairs, TriggerCounts: triggerCounts}
}

// emitPairs generates ordered event pairs within each group.
// It returns the set of trigger:condition type-pair keys emitted (ignoring scope)
// so callers can filter wider scopes.
func emitPairs(groups map[string][]baseEntry, scope string, seen map[string]bool, out *[]EventPair) map[string]bool {
	typePairKeys := make(map[string]bool)

	for _, group := range groups {
		// Collect distinct event types in this group.
		typeSet := make(map[string]bool)
		for _, e := range group {
			typeSet[e.eventType] = true
		}
		if len(typeSet) < 2 {
			continue
		}

		// Emit all ordered pairs of distinct event types.
		types := make([]string, 0, len(typeSet))
		for t := range typeSet {
			types = append(types, t)
		}

		for i := 0; i < len(types); i++ {
			for j := 0; j < len(types); j++ {
				if i == j {
					continue
				}
				trigger := types[i]
				condition := types[j]
				pair := EventPair{
					TriggerType:   trigger,
					ConditionType: condition,
					Scope:         scope,
				}
				pairKey := pair.Key()

				// Skip if already emitted (at this or tighter scope).
				if seen[pairKey] {
					continue
				}

				// Skip if the same type-pair was found at a tighter scope.
				typePairKey := trigger + ":" + condition
				if seen[typePairKey+":samePod"] || (scope == "sameNamespace" && seen[typePairKey+":sameNode"]) {
					continue
				}

				seen[pairKey] = true
				typePairKeys[typePairKey] = true
				*out = append(*out, pair)
			}
		}
	}
	return typePairKeys
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
