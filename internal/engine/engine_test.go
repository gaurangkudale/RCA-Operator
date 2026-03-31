package engine

import (
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const defaultRuleEngineName = "correlator"

func TestNewIncidentEngine_ResolvesDefaultRuleEngine(t *testing.T) {
	incidentEngine, err := NewIncidentEngine(fake.NewClientBuilder().Build(), nil, logr.Discard())
	if err != nil {
		t.Fatalf("NewIncidentEngine() error = %v", err)
	}
	if incidentEngine.RuleEngineName() != defaultRuleEngineName {
		t.Fatalf("RuleEngineName() = %q, want %q", incidentEngine.RuleEngineName(), defaultRuleEngineName)
	}
}

func TestNewIncidentEngine_FailsForUnknownRuleEngine(t *testing.T) {
	_, err := NewIncidentEngine(fake.NewClientBuilder().Build(), nil, logr.Discard(), WithRuleEngineName("missing"))
	if err == nil {
		t.Fatal("expected error for unknown rule engine")
	}
}
