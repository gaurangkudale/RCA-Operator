package correlator

import "github.com/gaurangkudale/rca-operator/internal/watcher"

// testRule builds a Rule implementation for use in unit tests. It lets tests
// inject arbitrary evaluation logic into a Correlator or Consumer without
// registering anything in the global (production) registry.
func testRule(name string, priority int, fn func(watcher.CorrelatorEvent, []Entry) CorrelationResult) Rule {
	return ruleDouble{name: name, priority: priority, evaluate: fn}
}

// ruleDouble is the test-only Rule implementation returned by testRule.
type ruleDouble struct {
	name     string
	priority int
	evaluate func(watcher.CorrelatorEvent, []Entry) CorrelationResult
}

func (r ruleDouble) Name() string     { return r.name }
func (r ruleDouble) Priority() int    { return r.priority }
func (r ruleDouble) Evaluate(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
	result := r.evaluate(event, entries)
	if result.Fired && result.Rule == "" {
		result.Rule = r.name
	}
	return result
}
