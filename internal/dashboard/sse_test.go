package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestSSEHub_SubscribeUnsubscribe(t *testing.T) {
	hub := NewSSEHub(logr.Discard())

	ch := hub.Subscribe("test")
	if hub.ClientCount("test") != 1 {
		t.Fatalf("expected 1 client, got %d", hub.ClientCount("test"))
	}

	// Subscribe a second client.
	ch2 := hub.Subscribe("test")
	if hub.ClientCount("test") != 2 {
		t.Fatalf("expected 2 clients, got %d", hub.ClientCount("test"))
	}

	// Unsubscribe first client.
	close(ch)
	hub.mu.Lock()
	delete(hub.clients["test"], ch)
	hub.mu.Unlock()
	if hub.ClientCount("test") != 1 {
		t.Fatalf("expected 1 client after unsubscribe, got %d", hub.ClientCount("test"))
	}

	close(ch2)
	hub.mu.Lock()
	delete(hub.clients["test"], ch2)
	hub.mu.Unlock()
	if hub.ClientCount("test") != 0 {
		t.Fatalf("expected 0 clients, got %d", hub.ClientCount("test"))
	}
}

func TestSSEHub_Broadcast(t *testing.T) {
	hub := NewSSEHub(logr.Discard())

	ch1 := hub.Subscribe("updates")
	ch2 := hub.Subscribe("updates")
	ch3 := hub.Subscribe("other") // Different channel

	hub.Broadcast("updates", SSEEvent{Type: "test", Data: "hello"})

	select {
	case ev := <-ch1:
		if ev.Type != "test" || ev.Data != "hello" {
			t.Errorf("ch1 unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("ch1 did not receive event")
	}

	select {
	case ev := <-ch2:
		if ev.Type != "test" {
			t.Errorf("ch2 unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("ch2 did not receive event")
	}

	// ch3 should NOT receive (different channel).
	select {
	case <-ch3:
		t.Error("ch3 should not receive event from different channel")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	close(ch1)
	close(ch2)
	close(ch3)
}

func TestSSEHub_BroadcastDropsOnFullBuffer(t *testing.T) {
	hub := NewSSEHub(logr.Discard())
	ch := hub.Subscribe("full")

	// Fill the buffer (capacity 32).
	for i := range 32 {
		hub.Broadcast("full", SSEEvent{Type: "fill", Data: i})
	}

	// This should not block even though buffer is full.
	hub.Broadcast("full", SSEEvent{Type: "overflow", Data: "dropped"})

	// Drain and count.
	count := 0
	for count < 32 {
		<-ch
		count++
	}

	// Buffer should be empty now, no overflow event.
	select {
	case <-ch:
		t.Error("should not have received overflow event")
	case <-time.After(50 * time.Millisecond):
	}

	close(ch)
}

func TestSSEHub_ServeHTTP(t *testing.T) {
	hub := NewSSEHub(logr.Discard())

	// Start the SSE handler in background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeHTTP(w, r.WithContext(ctx), "live")
	}))
	defer ts.Close()

	// Connect as SSE client.
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("failed to close response body: %v", err)
		}
	}()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", ct)
	}

	// Read the initial "connected" event.
	buf := make([]byte, 1024)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("failed to read: %v", err)
	}
	body := string(buf[:n])
	if !strings.Contains(body, "event: connected") {
		t.Errorf("expected connected event, got: %s", body)
	}

	// Broadcast an event and read it.
	hub.Broadcast("live", SSEEvent{Type: "topology.update", Data: map[string]string{"status": "ok"}})

	// Give a moment for the event to be written.
	time.Sleep(50 * time.Millisecond)
	n, err = resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("failed to read broadcast: %v", err)
	}
	body = string(buf[:n])
	if !strings.Contains(body, "event: topology.update") {
		t.Errorf("expected topology.update event, got: %s", body)
	}
	if !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("expected status:ok in data, got: %s", body)
	}

	cancel() // Disconnect
}

func TestSSEHub_ClientCountEmpty(t *testing.T) {
	hub := NewSSEHub(logr.Discard())
	if hub.ClientCount("nonexistent") != 0 {
		t.Error("expected 0 for nonexistent channel")
	}
}
