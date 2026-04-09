/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// SSEHub manages Server-Sent Event connections for live dashboard updates.
// Clients subscribe to named channels (e.g., "topology", "correlation")
// and receive JSON-encoded events pushed by the operator.
type SSEHub struct {
	mu      sync.RWMutex
	clients map[string]map[chan SSEEvent]struct{} // channel -> set of subscribers
	log     logr.Logger
}

// SSEEvent represents a single server-sent event.
type SSEEvent struct {
	Type string `json:"type"` // event type (e.g., "topology.update", "signal.new")
	Data any    `json:"data"` // JSON-serializable payload
}

// NewSSEHub creates a new SSE hub.
func NewSSEHub(log logr.Logger) *SSEHub {
	return &SSEHub{
		clients: make(map[string]map[chan SSEEvent]struct{}),
		log:     log.WithName("sse"),
	}
}

// Subscribe adds a client to a named channel and returns the event channel.
// The caller must call Unsubscribe when done.
func (h *SSEHub) Subscribe(channel string) chan SSEEvent {
	ch := make(chan SSEEvent, 32)
	h.mu.Lock()
	if h.clients[channel] == nil {
		h.clients[channel] = make(map[chan SSEEvent]struct{})
	}
	h.clients[channel][ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a client from a channel and closes its event channel.
func (h *SSEHub) Unsubscribe(channel string, ch chan SSEEvent) {
	h.mu.Lock()
	if subs, ok := h.clients[channel]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(h.clients, channel)
		}
	}
	h.mu.Unlock()
	// Drain remaining events to prevent goroutine leaks.
	for range ch {
	}
}

// Broadcast sends an event to all subscribers on a channel.
// Non-blocking: if a client's buffer is full, the event is dropped for that client.
func (h *SSEHub) Broadcast(channel string, event SSEEvent) {
	h.mu.RLock()
	subs := h.clients[channel]
	h.mu.RUnlock()

	for ch := range subs {
		select {
		case ch <- event:
		default:
			// Client buffer full, drop event
		}
	}
}

// ClientCount returns the number of active subscribers on a channel.
func (h *SSEHub) ClientCount(channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[channel])
}

// ServeHTTP handles an SSE connection for the given channel.
// It sets the appropriate headers and streams events until the client disconnects.
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request, channel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	ch := h.Subscribe(channel)
	defer func() {
		// Close the channel first, then unsubscribe (which drains).
		close(ch)
		h.mu.Lock()
		if subs, ok := h.clients[channel]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(h.clients, channel)
			}
		}
		h.mu.Unlock()
	}()

	// Send initial connection event.
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {\"channel\":%q,\"time\":%q}\n\n",
		channel, time.Now().UTC().Format(time.RFC3339)); err != nil {
		h.log.V(1).Info("failed to write connection event", "error", err)
	}
	flusher.Flush()

	// Keep-alive ticker to prevent proxy timeouts.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				h.log.V(1).Info("failed to marshal SSE event", "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
				h.log.V(1).Info("failed to write SSE event", "error", err)
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				h.log.V(1).Info("failed to write keepalive", "error", err)
				return
			}
			flusher.Flush()
		}
	}
}
