package rulengine

import (
	"strings"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gaurangkudale/rca-operator/internal/engine"
)

const crdEngineName = "crd"

// crdRuleEngineAdapter wraps CRDRuleEngine to implement the engine.RuleEngine interface.
type crdRuleEngineAdapter struct {
	*CRDRuleEngine
}

func (a crdRuleEngineAdapter) Name() string { return crdEngineName }

// Factory creates CRDRuleEngine instances for the engine factory registry.
type Factory struct {
	Client client.Client
	Logger logr.Logger
}

func (f Factory) Name() string  { return crdEngineName }
func (f Factory) Priority() int { return 200 } // Higher than old correlator (100)

func (f Factory) Supports(d engine.RuleEngineDiscovery) bool {
	return d.PreferredName == "" || strings.EqualFold(d.PreferredName, crdEngineName)
}

func (f Factory) Build(cfg engine.RuleEngineConfig) (engine.RuleEngine, error) {
	eng := NewCRDRuleEngine(f.Client, cfg.CorrelationWindow, f.Logger)
	return crdRuleEngineAdapter{eng}, nil
}
