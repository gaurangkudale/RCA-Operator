package rulengine

import (
	"context"
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
	// Engine holds the last built CRDRuleEngine so the controller can reload rules.
	Engine *CRDRuleEngine
}

func (f *Factory) Name() string  { return crdEngineName }
func (f *Factory) Priority() int { return 200 } // Higher than old correlator (100)

func (f *Factory) Supports(d engine.RuleEngineDiscovery) bool {
	return d.PreferredName == "" || strings.EqualFold(d.PreferredName, crdEngineName)
}

func (f *Factory) Build(cfg engine.RuleEngineConfig) (engine.RuleEngine, error) {
	eng := NewCRDRuleEngine(f.Client, cfg.CorrelationWindow, f.Logger)
	f.Engine = eng

	// Load rules at startup. Use the provided context or a background context.
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := eng.LoadRules(ctx); err != nil {
		// Log but don't fail — the engine will work with zero rules until
		// rules are created and the controller triggers a reload.
		f.Logger.WithName("crd-rule-engine-factory").Error(err, "Failed to load rules at startup (will retry on CRD changes)")
	}

	return crdRuleEngineAdapter{eng}, nil
}
