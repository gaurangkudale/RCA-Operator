package correlator

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// recordingRuleEngine records every event passed to Add so tests can assert
// which event types are routed through the rule engine.
type recordingRuleEngine struct {
	added []watcher.CorrelatorEvent
}

func (r *recordingRuleEngine) Add(e watcher.CorrelatorEvent) {
	r.added = append(r.added, e)
}

func (r *recordingRuleEngine) Evaluate(_ watcher.CorrelatorEvent) CorrelationResult {
	return CorrelationResult{}
}

// TestHandleEvent_LifecycleEventsBypassRuleEngine locks in the contract that
// PodHealthy / PodDeleted resolution events MUST NOT enter the rule engine's
// sliding-window buffer. Feeding them in pollutes correlation state and risks
// suppressing real failures that share a dedup key.
func TestHandleEvent_LifecycleEventsBypassRuleEngine(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	engine := &recordingRuleEngine{}
	consumer := NewConsumer(client, nil, logr.Discard(), WithRuleEngine(engine))

	cases := []watcher.CorrelatorEvent{
		watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{
			Namespace: "dev", PodName: "p1",
		}},
		watcher.PodDeletedEvent{BaseEvent: watcher.BaseEvent{
			Namespace: "dev", PodName: "p2",
		}},
	}
	for _, ev := range cases {
		// Returning an error here would mean the lifecycle bypass changed the
		// resolve path; we only care that the rule engine wasn't touched.
		_ = consumer.handleEvent(context.Background(), ev)
	}
	if got := len(engine.added); got != 0 {
		t.Fatalf("expected lifecycle events to bypass rule engine, but %d events were Added: %v", got, engine.added)
	}
}

// TestHandleEvent_FailureEventsStillReachRuleEngine guards against the bypass
// fix accidentally short-circuiting real signals.
func TestHandleEvent_FailureEventsStillReachRuleEngine(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	engine := &recordingRuleEngine{}
	consumer := NewConsumer(client, nil, logr.Discard(), WithRuleEngine(engine))

	ev := watcher.OOMKilledEvent{BaseEvent: watcher.BaseEvent{
		Namespace: "dev", PodName: "p1", AgentName: "a",
	}}
	_ = consumer.handleEvent(context.Background(), ev)

	if got := len(engine.added); got != 1 {
		t.Fatalf("expected failure event to reach rule engine once, got %d", got)
	}
}
