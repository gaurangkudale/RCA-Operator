package watcher

import "github.com/go-logr/logr"

// EventEmitter abstracts how watcher events are delivered to downstream consumers.
type EventEmitter interface {
	Emit(event CorrelatorEvent)
}

// ChannelEventEmitter sends watcher events to a shared correlator channel.
type ChannelEventEmitter struct {
	ch  chan<- CorrelatorEvent
	log logr.Logger
}

// NewChannelEventEmitter creates a non-blocking emitter backed by a channel.
func NewChannelEventEmitter(ch chan<- CorrelatorEvent, logger logr.Logger) *ChannelEventEmitter {
	return &ChannelEventEmitter{ch: ch, log: logger.WithName("event-emitter")}
}

// Emit attempts to send without blocking informer processing.
func (e *ChannelEventEmitter) Emit(event CorrelatorEvent) {
	select {
	case e.ch <- event:
	default:
		e.log.Info("Dropped watcher event because correlator channel is full", "eventType", event.Type(), "dedupKey", event.DedupKey())
	}
}
