package autodetect

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
)

// Detector periodically snapshots the correlation buffer, mines for
// co-occurring event patterns, and auto-creates RCACorrelationRule CRDs
// when confidence thresholds are met.
type Detector struct {
	buffer      *correlator.Buffer
	accumulator *Accumulator
	creator     *Creator
	config      Config
	log         logr.Logger
	seeded      bool
}

// NewDetector returns a Detector ready to run.
func NewDetector(buf *correlator.Buffer, c client.Client, cfg Config, logger logr.Logger) *Detector {
	return &Detector{
		buffer:      buf,
		accumulator: NewAccumulator(),
		creator:     NewCreator(c, cfg, logger),
		config:      cfg,
		log:         logger.WithName("autodetect"),
	}
}

// Run blocks until ctx is cancelled, ticking at AnalysisInterval.
func (d *Detector) Run(ctx context.Context) {
	d.log.Info("Auto-detect started",
		"interval", d.config.AnalysisInterval,
		"minOccurrences", d.config.MinOccurrences,
		"confidence", d.config.ConfidenceThreshold,
		"maxRules", d.config.MaxAutoRules,
		"expiry", d.config.ExpiryDuration,
	)

	ticker := time.NewTicker(d.config.AnalysisInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("Auto-detect stopped")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

// tick performs one analysis cycle.
func (d *Detector) tick(ctx context.Context) {
	start := time.Now()

	// Seed accumulator from existing auto-rules on first tick.
	if !d.seeded {
		d.seedFromExisting(ctx)
		d.seeded = true
	}

	// 1. Snapshot the buffer.
	entries := d.buffer.Snapshot()
	if len(entries) == 0 {
		return
	}

	// 2. Mine patterns.
	result := MinePatterns(entries)

	// 3. Record in accumulator.
	d.accumulator.Record(result.Pairs, result.TriggerCounts)

	// 4. Create rules for ready patterns.
	ready := d.accumulator.ReadyPatterns(d.config)
	rulesCreatedThisTick := 0
	for _, rec := range ready {
		if rec.RuleName != "" {
			// Already has a rule — just update annotations.
			if err := d.creator.EnsureRule(ctx, rec); err != nil {
				d.log.Error(err, "Failed to update auto-rule", "pattern", rec.Pair.Key())
			}
			continue
		}
		if err := d.creator.EnsureRule(ctx, rec); err != nil {
			d.log.Error(err, "Failed to create auto-rule", "pattern", rec.Pair.Key())
		} else if rec.RuleName != "" {
			rulesCreatedThisTick++
			RecordRuleCreated()
		}
	}

	// 5. Expire stale rules.
	activePatterns := d.accumulator.All()
	countBefore, _ := d.creator.CountAutoRules(ctx)
	if err := d.creator.ExpireStaleRules(ctx, activePatterns); err != nil {
		d.log.Error(err, "Failed to expire stale auto-rules")
	}
	countAfter, _ := d.creator.CountAutoRules(ctx)
	expired := countBefore - countAfter
	for range expired {
		RecordRuleExpired()
	}

	// 6. Update metrics.
	SetPatternsTracked(d.accumulator.Count())
	SetRulesActive(countAfter)
	ObserveAnalysisDuration(time.Since(start).Seconds())

	if rulesCreatedThisTick > 0 || expired > 0 {
		d.log.Info("Analysis tick completed",
			"patterns", d.accumulator.Count(),
			"readyPatterns", len(ready),
			"rulesCreated", rulesCreatedThisTick,
			"rulesExpired", expired,
			"activeRules", countAfter,
			"duration", time.Since(start).String(),
		)
	}
}

// seedFromExisting loads previously created auto-rules to prevent duplicates
// and premature expiry on restart.
func (d *Detector) seedFromExisting(ctx context.Context) {
	records, err := d.creator.LoadExisting(ctx)
	if err != nil {
		d.log.Error(err, "Failed to load existing auto-rules for seeding")
		return
	}
	for key, rec := range records {
		d.accumulator.Seed(key, rec)
	}
	if len(records) > 0 {
		d.log.Info("Seeded accumulator from existing auto-rules", "count", len(records))
	}
}
