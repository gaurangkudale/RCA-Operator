package correlator

import (
	"testing"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func TestBuffer_Subscribe_DeliversEntries(t *testing.T) {
	b := NewBuffer(5 * time.Minute)
	ch := make(chan Entry, 4)
	unsub := b.Subscribe(ch)
	defer unsub()

	b.Add(crashLoop("ns1", "pod1", "n1", "c1", 3))
	b.Add(oomKilled("ns1", "pod1", "n1", "c1"))

	for i, want := range []watcher.EventType{watcher.EventTypeCrashLoopBackOff, watcher.EventTypeOOMKilled} {
		select {
		case got := <-ch:
			if got.Event.Type() != want {
				t.Fatalf("event %d: got %s, want %s", i, got.Event.Type(), want)
			}
		case <-time.After(time.Second):
			t.Fatalf("event %d: timed out waiting for delivery", i)
		}
	}
}

func TestBuffer_Subscribe_NonBlockingOnFullChannel(t *testing.T) {
	b := NewBuffer(5 * time.Minute)
	ch := make(chan Entry, 1)
	unsub := b.Subscribe(ch)
	defer unsub()

	// First Add fills the buffered channel; subsequent Adds must NOT block.
	done := make(chan struct{})
	go func() {
		for i := range 10 {
			b.Add(crashLoop("ns", "pod", "n", "c", int32(i)))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Add blocked on full subscriber channel")
	}
}

func TestBuffer_Subscribe_Unsubscribe(t *testing.T) {
	b := NewBuffer(5 * time.Minute)
	ch := make(chan Entry, 4)
	unsub := b.Subscribe(ch)

	b.Add(crashLoop("ns", "p", "n", "c", 1))
	<-ch
	unsub()
	b.Add(oomKilled("ns", "p", "n", "c"))
	select {
	case e := <-ch:
		t.Fatalf("got event after unsubscribe: %s", e.Event.Type())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClassifyEvent(t *testing.T) {
	cases := []struct {
		name     string
		event    watcher.CorrelatorEvent
		wantKind string
	}{
		{"crashloop", crashLoop("ns", "p", "n", "c", 3), "k8s"},
		{"oom", oomKilled("ns", "p", "n", "c"), "k8s"},
		{"otel-log", watcher.OTelLogMatchEvent{ServiceName: "svc", Severity: "ERROR", Body: "boom"}, "log"},
		{"otel-span-error", watcher.OTelSpanErrorEvent{ServiceName: "svc", SpanName: "GET /x"}, "trace"},
		{"otel-latency", watcher.OTelSpanLatencySpikeEvent{ServiceName: "svc", SpanName: "q"}, "trace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyEvent(tc.event)
			if c.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", c.Kind, tc.wantKind)
			}
			if c.Title == "" {
				t.Fatalf("empty Title for %s", tc.name)
			}
		})
	}
}
