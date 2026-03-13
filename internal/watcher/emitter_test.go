package watcher

import (
	"testing"

	"github.com/go-logr/logr"
)

// TestChannelEventEmitter_SendsToChannel verifies that Emit forwards the event to
// the underlying channel when capacity is available.
func TestChannelEventEmitter_SendsToChannel(t *testing.T) {
	ch := make(chan CorrelatorEvent, 4)
	em := NewChannelEventEmitter(ch, logr.Discard())

	ev := PodHealthyEvent{BaseEvent: BaseEvent{Namespace: "dev", PodName: "pod-a"}}
	em.Emit(ev)

	select {
	case got := <-ch:
		if got.Type() != EventTypePodHealthy {
			t.Errorf("got event type %q, want PodHealthy", got.Type())
		}
	default:
		t.Fatal("expected event in channel but channel is empty")
	}
}

// TestChannelEventEmitter_DropsWhenFull verifies that Emit does not block and
// silently drops events when the channel is full.
func TestChannelEventEmitter_DropsWhenFull(t *testing.T) {
	ch := make(chan CorrelatorEvent, 1)
	em := NewChannelEventEmitter(ch, logr.Discard())

	ev := PodHealthyEvent{BaseEvent: BaseEvent{Namespace: "dev", PodName: "pod-a"}}
	em.Emit(ev) // fills the channel
	em.Emit(ev) // should be dropped non-blocking, not panic

	// Only one event should be in the channel.
	count := len(ch)
	if count != 1 {
		t.Errorf("expected 1 event in channel (second dropped), got %d", count)
	}
}
