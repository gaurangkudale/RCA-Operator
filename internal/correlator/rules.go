package correlator

import "github.com/gaurangkudale/rca-operator/internal/watcher"

// ruleFunc is the function signature for correlation rule evaluation logic.
// All correlation rules are loaded dynamically from RCACorrelationRule CRDs
// by the CRD rule engine — no hardcoded rules are registered here.
type ruleFunc func(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult
