package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gaurangkudale/rca-operator/internal/collectors"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
)

type Option func(*incidentEngineOptions)

type incidentEngineOptions struct {
	correlationWindow  time.Duration
	consumerOptions    []correlator.Option
	preferredRuleName  string
	resolvedRuleEngine RuleEngine
	context            context.Context
}

// RuleEngine is the engine-owned contract for correlated incident evaluation.
type RuleEngine interface {
	correlator.RuleEngine
	Name() string
}

// RuleEngineFactory builds a rule engine implementation that the incident
// engine can discover at runtime.
type RuleEngineFactory interface {
	Name() string
	Priority() int
	Supports(RuleEngineDiscovery) bool
	Build(RuleEngineConfig) (RuleEngine, error)
}

type RuleEngineDiscovery struct {
	PreferredName string
}

// RuleEngineConfig carries runtime settings into a resolved rule engine.
type RuleEngineConfig struct {
	CorrelationWindow time.Duration
	Context           context.Context
}

var (
	ruleEngineFactoryMu sync.RWMutex
	ruleEngineFactories []RuleEngineFactory
)

// RegisterRuleEngineFactory makes a rule engine available for automatic
// discovery by the incident engine.
func RegisterRuleEngineFactory(factory RuleEngineFactory) {
	if factory == nil {
		panic("engine: cannot register nil rule engine factory")
	}

	ruleEngineFactoryMu.Lock()
	defer ruleEngineFactoryMu.Unlock()

	for _, existing := range ruleEngineFactories {
		if strings.EqualFold(existing.Name(), factory.Name()) {
			panic(fmt.Sprintf("engine: duplicate rule engine factory %q", factory.Name()))
		}
	}

	ruleEngineFactories = append(ruleEngineFactories, factory)
}

func registeredRuleEngineFactories() []RuleEngineFactory {
	ruleEngineFactoryMu.RLock()
	defer ruleEngineFactoryMu.RUnlock()

	factories := append([]RuleEngineFactory(nil), ruleEngineFactories...)
	sort.SliceStable(factories, func(i, j int) bool {
		if factories[i].Priority() == factories[j].Priority() {
			return strings.ToLower(factories[i].Name()) < strings.ToLower(factories[j].Name())
		}
		return factories[i].Priority() > factories[j].Priority()
	})
	return factories
}

// WithCorrelationWindow overrides the temporary correlation window used by the
// currently resolved rule engine.
func WithCorrelationWindow(window time.Duration) Option {
	return func(opts *incidentEngineOptions) {
		if window > 0 {
			opts.correlationWindow = window
		}
	}
}

// WithRuleEngine allows explicit injection of a rule engine implementation.
func WithRuleEngine(ruleEngine RuleEngine) Option {
	return func(opts *incidentEngineOptions) {
		opts.resolvedRuleEngine = ruleEngine
	}
}

// WithRuleEngineName asks the incident engine to resolve a specific registered
// rule engine. When unset, the incident engine auto-selects the highest-priority
// compatible engine.
func WithRuleEngineName(name string) Option {
	return func(opts *incidentEngineOptions) {
		opts.preferredRuleName = strings.TrimSpace(name)
	}
}

// WithContext sets the context used for rule engine initialization (e.g. loading CRD rules at startup).
func WithContext(ctx context.Context) Option {
	return func(opts *incidentEngineOptions) {
		opts.context = ctx
	}
}

// WithConsumerOption appends a low-level consumer option for compatibility
// while the incident engine surface is being normalized.
func WithConsumerOption(opt correlator.Option) Option {
	return func(opts *incidentEngineOptions) {
		opts.consumerOptions = append(opts.consumerOptions, opt)
	}
}

// WithEventRecorder forwards lifecycle events through the incident engine's
// consumer compatibility layer.
func WithEventRecorder(recorder events.EventRecorder) Option {
	return func(opts *incidentEngineOptions) {
		opts.consumerOptions = append(opts.consumerOptions, correlator.WithEventRecorder(recorder))
	}
}

// WithCrossSignalEnricher attaches a cross-signal enricher that queries
// external telemetry backends to enrich incidents with traces and blast radius.
func WithCrossSignalEnricher(e *correlator.CrossSignalEnricher) Option {
	return func(opts *incidentEngineOptions) {
		opts.consumerOptions = append(opts.consumerOptions, correlator.WithCrossSignalEnricher(e))
	}
}

// IncidentEngine is the runtime bridge from collected signals to durable
// incident state.
type IncidentEngine struct {
	consumer       *correlator.Consumer
	ruleEngineName string
}

// NewIncidentEngine resolves a rule engine automatically and wires it into the
// incident processing runtime.
func NewIncidentEngine(
	c client.Client,
	signals <-chan collectors.Signal,
	logger logr.Logger,
	opts ...Option,
) (*IncidentEngine, error) {
	options := incidentEngineOptions{
		correlationWindow: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(&options)
	}

	ruleEngine, err := resolveRuleEngine(options)
	if err != nil {
		return nil, err
	}

	consumerOptions := append([]correlator.Option{correlator.WithRuleEngine(ruleEngine)}, options.consumerOptions...)

	return &IncidentEngine{
		consumer:       correlator.NewConsumer(c, signals, logger, consumerOptions...),
		ruleEngineName: ruleEngine.Name(),
	}, nil
}

func resolveRuleEngine(options incidentEngineOptions) (RuleEngine, error) {
	if options.resolvedRuleEngine != nil {
		return options.resolvedRuleEngine, nil
	}

	discovery := RuleEngineDiscovery{
		PreferredName: options.preferredRuleName,
	}

	for _, factory := range registeredRuleEngineFactories() {
		if !factory.Supports(discovery) {
			continue
		}
		engine, err := factory.Build(RuleEngineConfig{
			CorrelationWindow: options.correlationWindow,
			Context:           options.context,
		})
		if err != nil {
			return nil, fmt.Errorf("build rule engine %q: %w", factory.Name(), err)
		}
		return engine, nil
	}

	if options.preferredRuleName != "" {
		return nil, fmt.Errorf("no registered rule engine matched %q", options.preferredRuleName)
	}
	return nil, fmt.Errorf("no registered rule engine available")
}

func (e *IncidentEngine) RuleEngineName() string {
	if e == nil {
		return ""
	}
	return e.ruleEngineName
}

func (e *IncidentEngine) Run(ctx context.Context) {
	if e == nil || e.consumer == nil {
		return
	}
	e.consumer.Run(ctx)
}

// Start implements manager.Runnable and runs the incident engine loop.
func (e *IncidentEngine) Start(ctx context.Context) error {
	e.Run(ctx)
	return nil
}

// NeedLeaderElection ensures only the active leader runs the incident engine.
func (e *IncidentEngine) NeedLeaderElection() bool {
	return true
}
