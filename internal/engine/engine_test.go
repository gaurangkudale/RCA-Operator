package engine

import (
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// testRuleEngine is a minimal rule engine for testing the factory resolution
// pipeline without importing the real rulengine package (which would cycle).
type testRuleEngine struct{}

func (testRuleEngine) Name() string                  { return "test-crd" }
func (testRuleEngine) Add(_ watcher.CorrelatorEvent) {}
func (testRuleEngine) Evaluate(_ watcher.CorrelatorEvent) correlator.CorrelationResult {
	return correlator.CorrelationResult{}
}

type testRuleEngineFactory struct{}

func (testRuleEngineFactory) Name() string  { return "test-crd" }
func (testRuleEngineFactory) Priority() int { return 200 }
func (testRuleEngineFactory) Supports(d RuleEngineDiscovery) bool {
	return d.PreferredName == "" || d.PreferredName == "test-crd"
}
func (testRuleEngineFactory) Build(_ RuleEngineConfig) (RuleEngine, error) {
	return testRuleEngine{}, nil
}

func TestNewIncidentEngine_ResolvesDefaultRuleEngine(t *testing.T) {
	RegisterRuleEngineFactory(testRuleEngineFactory{})
	t.Cleanup(func() {
		ruleEngineFactoryMu.Lock()
		defer ruleEngineFactoryMu.Unlock()
		filtered := make([]RuleEngineFactory, 0, len(ruleEngineFactories))
		for _, f := range ruleEngineFactories {
			if f.Name() != "test-crd" {
				filtered = append(filtered, f)
			}
		}
		ruleEngineFactories = filtered
	})

	incidentEngine, err := NewIncidentEngine(fake.NewClientBuilder().Build(), nil, logr.Discard())
	if err != nil {
		t.Fatalf("NewIncidentEngine() error = %v", err)
	}
	if incidentEngine.RuleEngineName() != "test-crd" {
		t.Fatalf("RuleEngineName() = %q, want %q", incidentEngine.RuleEngineName(), "test-crd")
	}
}

func TestNewIncidentEngine_FailsForUnknownRuleEngine(t *testing.T) {
	_, err := NewIncidentEngine(fake.NewClientBuilder().Build(), nil, logr.Discard(), WithRuleEngineName("missing"))
	if err == nil {
		t.Fatal("expected error for unknown rule engine")
	}
}
