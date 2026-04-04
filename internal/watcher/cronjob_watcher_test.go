package watcher

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCronJobWatcher_IsOwnedByCronJob(t *testing.T) {
	cronJobUID := types.UID("cj-uid-1")

	ownedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cronjob-12345",
			Namespace: "production",
			OwnerReferences: []metav1.OwnerReference{
				{UID: cronJobUID, Kind: "CronJob", Name: "my-cronjob"},
			},
		},
	}

	if !isOwnedByCronJob(ownedJob, cronJobUID) {
		t.Error("expected job to be owned by cronjob")
	}

	unownedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-job",
			Namespace: "production",
			OwnerReferences: []metav1.OwnerReference{
				{UID: "other-uid", Kind: "CronJob", Name: "other-cronjob"},
			},
		},
	}

	if isOwnedByCronJob(unownedJob, cronJobUID) {
		t.Error("expected job to NOT be owned by cronjob")
	}
}

func TestCronJobWatcher_MarkFailAlerted_Dedup(t *testing.T) {
	w := &CronJobWatcher{
		failAlerted: make(map[types.UID]struct{}),
	}

	uid := types.UID("job-uid-1")

	if !w.markFailAlerted(uid) {
		t.Error("first call should return true")
	}
	if w.markFailAlerted(uid) {
		t.Error("second call should return false (dedup)")
	}
}

func TestCronJobWatcher_ShouldWatchNamespace(t *testing.T) {
	w := &CronJobWatcher{
		namespaceSet: toNamespaceSet([]string{"production"}),
	}

	if !w.shouldWatchNamespace("production") {
		t.Error("should watch production")
	}
	if w.shouldWatchNamespace("staging") {
		t.Error("should not watch staging")
	}

	wAll := &CronJobWatcher{
		namespaceSet: toNamespaceSet(nil),
	}
	if !wAll.shouldWatchNamespace("anything") {
		t.Error("empty namespace set should watch all")
	}
}

func TestCronJobFailedEvent_DedupKey(t *testing.T) {
	ev := CronJobFailedEvent{
		BaseEvent:   BaseEvent{Namespace: "production"},
		CronJobName: "daily-backup",
	}
	want := "CronJobFailed:production:daily-backup"
	if got := ev.DedupKey(); got != want {
		t.Errorf("DedupKey: want %q, got %q", want, got)
	}
}

func TestCronJobFailedEvent_Type(t *testing.T) {
	ev := CronJobFailedEvent{}
	if ev.Type() != EventTypeCronJobFailed {
		t.Errorf("Type: want %q, got %q", EventTypeCronJobFailed, ev.Type())
	}
}

func TestJobFailedCondition_DetectsFailure(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC)

	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					Reason:             "BackoffLimitExceeded",
					LastTransitionTime: metav1.NewTime(now),
				},
			},
		},
	}

	cond, ok := jobFailedCondition(job)
	if !ok {
		t.Fatal("expected jobFailedCondition to return true")
	}
	if cond.Reason != "BackoffLimitExceeded" {
		t.Errorf("Reason: want %q, got %q", "BackoffLimitExceeded", cond.Reason)
	}
}

func TestJobFailedCondition_CompletedJob(t *testing.T) {
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	_, ok := jobFailedCondition(job)
	if ok {
		t.Error("expected jobFailedCondition to return false for completed job")
	}
}
