package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func newTestJobWatcher(namespaces []string) (*JobWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewJobWatcher(nil, em, logr.Discard(), JobWatcherConfig{
		AgentName:       "agent-test",
		WatchNamespaces: namespaces,
	})
	return w, em
}

func failedJob(namespace, name, uid, reason, message string, condTime time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: metav1.NewTime(condTime),
				},
			},
		},
	}
}

func completedJob(namespace, name, uid string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func TestJobWatcher_DetectsFailedJob(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC)
	w, em := newTestJobWatcher(nil)
	w.clock = func() time.Time { return now }

	job := failedJob("production", "migration-job", "uid-1", "BackoffLimitExceeded", "Job has reached the specified backoff limit", now)
	w.detectFailed(job)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(JobFailedEvent)
	if !ok {
		t.Fatalf("expected JobFailedEvent, got %T", em.events[0])
	}
	if ev.JobName != "migration-job" {
		t.Errorf("JobName: want %q, got %q", "migration-job", ev.JobName)
	}
	if ev.Reason != "BackoffLimitExceeded" {
		t.Errorf("Reason: want %q, got %q", "BackoffLimitExceeded", ev.Reason)
	}
	if !ev.At.Equal(now) {
		t.Errorf("At: want %v, got %v", now, ev.At)
	}
}

func TestJobWatcher_DedupSameUID(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 5, 0, 0, time.UTC)
	w, em := newTestJobWatcher(nil)
	w.clock = func() time.Time { return now }

	job := failedJob("production", "migration-job", "uid-dedup", "BackoffLimitExceeded", "limit", now)
	w.detectFailed(job)
	w.detectFailed(job)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event (dedup), got %d", len(em.events))
	}
}

func TestJobWatcher_CompletedJob_NoEvent(t *testing.T) {
	w, em := newTestJobWatcher(nil)
	job := completedJob("production", "migration-job", "uid-complete")
	w.detectFailed(job)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for completed job, got %d", len(em.events))
	}
}

func TestJobWatcher_NamespaceFilter(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 20, 0, 0, time.UTC)
	w, em := newTestJobWatcher([]string{"production"})
	w.clock = func() time.Time { return now }

	job := failedJob("staging", "migration-job", "uid-ns", "BackoffLimitExceeded", "limit", now)
	w.onAdd(job)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for unwatched namespace, got %d", len(em.events))
	}
}

func TestJobWatcher_FallbackTimestamp(t *testing.T) {
	fallback := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	w, em := newTestJobWatcher(nil)
	w.clock = func() time.Time { return fallback }

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "production", UID: "uid-ts"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
					Reason: "DeadlineExceeded",
					// LastTransitionTime intentionally zero.
				},
			},
		},
	}
	w.detectFailed(job)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev := em.events[0].(JobFailedEvent)
	if !ev.At.Equal(fallback) {
		t.Errorf("At: want fallback %v, got %v", fallback, ev.At)
	}
}

func TestJobWatcher_DedupKey(t *testing.T) {
	ev := JobFailedEvent{
		BaseEvent: BaseEvent{Namespace: "production"},
		JobName:   "migration-job",
	}
	want := "JobFailed:production:migration-job"
	if got := ev.DedupKey(); got != want {
		t.Errorf("DedupKey: want %q, got %q", want, got)
	}
}
