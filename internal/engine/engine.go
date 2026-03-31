package engine

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gaurangkudale/rca-operator/internal/collectors"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
)

type Option func(*incidentEngineOptions)

type incidentEngineOptions struct {
	correlationWindow time.Duration
	correlatorOptions []correlator.Option
}

// WithCorrelationWindow overrides the temporary correlation window used by the
// current engine bridge while the normalized signal engine is introduced.
func WithCorrelationWindow(window time.Duration) Option {
	return func(opts *incidentEngineOptions) {
		if window > 0 {
			opts.correlationWindow = window
		}
	}
}

func WithCorrelatorOption(opt correlator.Option) Option {
	return func(opts *incidentEngineOptions) {
		opts.correlatorOptions = append(opts.correlatorOptions, opt)
	}
}

// IncidentEngine is the current runtime bridge from collected signals to
// durable incident state. The internals still delegate to the existing
// correlator/reporter implementation while the engine refactor proceeds.
type IncidentEngine struct {
	consumer *correlator.Consumer
}

func NewIncidentEngine(
	c client.Client,
	signals <-chan collectors.Signal,
	logger logr.Logger,
	opts ...Option,
) *IncidentEngine {
	options := incidentEngineOptions{
		correlationWindow: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(&options)
	}

	corr := correlator.NewCorrelator(options.correlationWindow)
	correlatorOptions := append([]correlator.Option{correlator.WithCorrelator(corr)}, options.correlatorOptions...)

	return &IncidentEngine{
		consumer: correlator.NewConsumer(c, signals, logger, correlatorOptions...),
	}
}

func (e *IncidentEngine) Run(ctx context.Context) {
	if e == nil || e.consumer == nil {
		return
	}
	e.consumer.Run(ctx)
}
