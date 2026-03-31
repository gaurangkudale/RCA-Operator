package watcher

import (
	"testing"
	"time"
)

// TestEventTypesAndKeys exercises the Type(), OccurredAt(), and DedupKey()
// methods on every concrete event struct, providing coverage for all
// one-liner interface implementations.
func TestEventTypesAndKeys(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)

	events := []struct {
		name     string
		event    CorrelatorEvent
		wantType EventType
		wantKey  string
	}{
		{
			name: "CrashLoopBackOffEvent",
			event: CrashLoopBackOffEvent{
				BaseEvent:     BaseEvent{At: now, Namespace: "dev", PodName: "pod-a"},
				ContainerName: "app",
			},
			wantType: EventTypeCrashLoopBackOff,
			wantKey:  "CrashLoopBackOff:dev:pod-a:app",
		},
		{
			name: "OOMKilledEvent",
			event: OOMKilledEvent{
				BaseEvent:     BaseEvent{At: now, Namespace: "dev", PodName: "pod-b", PodUID: "uid-b"},
				ContainerName: "app",
			},
			wantType: EventTypeOOMKilled,
			wantKey:  "OOMKilled:dev:pod-b:app:uid-b",
		},
		{
			name: "ImagePullBackOffEvent",
			event: ImagePullBackOffEvent{
				BaseEvent:     BaseEvent{At: now, Namespace: "dev", PodName: "pod-c"},
				ContainerName: "app",
			},
			wantType: EventTypeImagePullBackOff,
			wantKey:  "ImagePullBackOff:dev:pod-c:app",
		},
		{
			name: "PodPendingTooLongEvent",
			event: PodPendingTooLongEvent{
				BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-d", PodUID: "uid-d"},
			},
			wantType: EventTypePodPendingTooLong,
			wantKey:  "PodPendingTooLong:dev:pod-d:uid-d",
		},
		{
			name: "GracePeriodViolationEvent",
			event: GracePeriodViolationEvent{
				BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-e", PodUID: "uid-e"},
			},
			wantType: EventTypeGracePeriodViolation,
			wantKey:  "GracePeriodViolation:dev:pod-e:uid-e",
		},
		{
			name:     "PodHealthyEvent",
			event:    PodHealthyEvent{BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-f"}},
			wantType: EventTypePodHealthy,
			wantKey:  "PodHealthy:dev:pod-f",
		},
		{
			name:     "PodDeletedEvent",
			event:    PodDeletedEvent{BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-g"}},
			wantType: EventTypePodDeleted,
			wantKey:  "PodDeleted:dev:pod-g",
		},
		{
			name: "NodeNotReadyEvent",
			event: NodeNotReadyEvent{
				BaseEvent: BaseEvent{At: now, Namespace: "dev", NodeName: "node-1"},
			},
			wantType: EventTypeNodeNotReady,
			wantKey:  "NodeNotReady:dev:node-1",
		},
		{
			name: "PodEvictedEvent",
			event: PodEvictedEvent{
				BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-h", PodUID: "uid-h"},
			},
			wantType: EventTypePodEvicted,
			wantKey:  "PodEvicted:dev:pod-h:uid-h",
		},
		{
			name: "ProbeFailureEvent",
			event: ProbeFailureEvent{
				BaseEvent: BaseEvent{At: now, Namespace: "dev", PodName: "pod-i"},
				ProbeType: "Liveness",
			},
			wantType: EventTypeProbeFailure,
			wantKey:  "ProbeFailure:dev:pod-i:Liveness",
		},
		{
			name: "StalledRolloutEvent",
			event: StalledRolloutEvent{
				BaseEvent:      BaseEvent{At: now, Namespace: "dev"},
				DeploymentName: "my-deploy",
			},
			wantType: EventTypeStalledRollout,
			wantKey:  "StalledRollout:dev:my-deploy",
		},
		{
			name: "NodePressureEvent",
			event: NodePressureEvent{
				BaseEvent:    BaseEvent{At: now, NodeName: "node-2"},
				PressureType: "DiskPressure",
			},
			wantType: EventTypeNodePressure,
			wantKey:  "NodePressure:node-2:DiskPressure",
		},
	}

	for _, tc := range events {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.event.Type(); got != tc.wantType {
				t.Errorf("Type(): got %q, want %q", got, tc.wantType)
			}
			if got := tc.event.OccurredAt(); !got.Equal(now) {
				t.Errorf("OccurredAt(): got %v, want %v", got, now)
			}
			if got := tc.event.DedupKey(); got != tc.wantKey {
				t.Errorf("DedupKey(): got %q, want %q", got, tc.wantKey)
			}
		})
	}
}
