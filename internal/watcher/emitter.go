package watcher

import (
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// EventEmitter abstracts how watcher events are delivered to downstream consumers.
type EventEmitter interface {
	Emit(event CorrelatorEvent)
}

// ChannelEventEmitter sends watcher events to a shared correlator channel.
type ChannelEventEmitter struct {
	ch          chan<- CorrelatorEvent
	log         logr.Logger
	dedupWindow time.Duration

	mu     sync.Mutex
	recent map[string]time.Time
}

type ChannelEventEmitterOptions struct {
	// DedupWindow coalesces repeated events with the same DedupKey before they
	// enter the shared channel. This protects the incident engine from OTLP log
	// storms while still allowing fresh signals through after the window.
	DedupWindow time.Duration
}

// NewChannelEventEmitter creates a non-blocking emitter backed by a channel.
func NewChannelEventEmitter(ch chan<- CorrelatorEvent, logger logr.Logger) *ChannelEventEmitter {
	return NewChannelEventEmitterWithOptions(ch, logger, ChannelEventEmitterOptions{})
}

func NewChannelEventEmitterWithOptions(ch chan<- CorrelatorEvent, logger logr.Logger, opts ChannelEventEmitterOptions) *ChannelEventEmitter {
	return &ChannelEventEmitter{
		ch:          ch,
		log:         logger.WithName("event-emitter"),
		dedupWindow: opts.DedupWindow,
		recent:      map[string]time.Time{},
	}
}

// Emit attempts to send without blocking informer processing.
func (e *ChannelEventEmitter) Emit(event CorrelatorEvent) {
	if e == nil || event == nil {
		return
	}
	if e.shouldDropDuplicate(event) {
		return
	}
	select {
	case e.ch <- event:
	default:
		e.log.Info("Dropped watcher event because correlator channel is full", "eventType", event.Type(), "dedupKey", event.DedupKey())
	}
}

func (e *ChannelEventEmitter) shouldDropDuplicate(event CorrelatorEvent) bool {
	if e.dedupWindow <= 0 {
		return false
	}
	key := event.DedupKey()
	if key == "" {
		return false
	}
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, ok := e.recent[key]; ok && now.Sub(last) < e.dedupWindow {
		return true
	}
	e.recent[key] = now
	for k, seenAt := range e.recent {
		if now.Sub(seenAt) >= e.dedupWindow {
			delete(e.recent, k)
		}
	}
	return false
}
