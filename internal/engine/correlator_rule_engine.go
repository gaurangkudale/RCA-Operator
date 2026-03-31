package engine

import (
	"strings"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
)

type correlatorRuleEngine struct {
	*correlator.Correlator
}

func (c correlatorRuleEngine) Name() string {
	return "correlator"
}

type correlatorRuleEngineFactory struct{}

func (correlatorRuleEngineFactory) Name() string {
	return "correlator"
}

func (correlatorRuleEngineFactory) Priority() int {
	return 100
}

func (correlatorRuleEngineFactory) Supports(discovery RuleEngineDiscovery) bool {
	return discovery.PreferredName == "" || strings.EqualFold(discovery.PreferredName, "correlator")
}

func (correlatorRuleEngineFactory) Build(cfg RuleEngineConfig) (RuleEngine, error) {
	return correlatorRuleEngine{
		Correlator: correlator.NewCorrelator(cfg.CorrelationWindow),
	}, nil
}

func init() {
	RegisterRuleEngineFactory(correlatorRuleEngineFactory{})
}
