package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultCronJobScanInterval = 30 * time.Second
)

// CronJobWatcherConfig controls the behaviour of the CronJob failure watcher.
type CronJobWatcherConfig struct {
	AgentName       string
	WatchNamespaces []string
	ScanInterval    time.Duration
}

// CronJobWatcher monitors batch/v1 CronJobs and emits a CronJobFailedEvent when
// the most recent child Job has failed, indicating a broken scheduled task.
//
// At most one event is emitted per failed child Job (keyed by Job UID).
type CronJobWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  CronJobWatcherConfig
	clock   func() time.Time

	mu           sync.Mutex
	failAlerted  map[types.UID]struct{} // keyed by child Job UID
	namespaceSet map[string]struct{}
}

// NewCronJobWatcher creates a CronJobWatcher backed by a controller-runtime cache.
func NewCronJobWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg CronJobWatcherConfig) *CronJobWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultCronJobScanInterval
	}

	return &CronJobWatcher{
		cache:        cache,
		emitter:      emitter,
		log:          logger.WithName("cronjob-watcher"),
		config:       cfg,
		clock:        time.Now,
		failAlerted:  make(map[types.UID]struct{}),
		namespaceSet: toNamespaceSet(cfg.WatchNamespaces),
	}
}

// Start registers a periodic scanner that checks for CronJobs whose most recent
// child Job has failed. Non-blocking; all goroutines are bounded by ctx.
//
// Unlike other watchers, CronJobWatcher relies on scanning Jobs rather than
// watching CronJob objects directly, because the failure signal comes from the
// child Job's status, not from the CronJob spec.
func (w *CronJobWatcher) Start(ctx context.Context) error {
	// We need both CronJob and Job informers.
	if _, err := w.cache.GetInformer(ctx, &batchv1.CronJob{}); err != nil {
		return fmt.Errorf("failed to get cronjob informer: %w", err)
	}
	if _, err := w.cache.GetInformer(ctx, &batchv1.Job{}); err != nil {
		return fmt.Errorf("failed to get job informer for cronjob watcher: %w", err)
	}

	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("CronJob watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.scanCronJobs(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanCronJobs, w.config.ScanInterval)

	w.log.Info("Started cronjob watcher", "scanInterval", w.config.ScanInterval.String())
	return nil
}

// scanCronJobs lists all CronJobs and checks their most recent child Jobs for failures.
func (w *CronJobWatcher) scanCronJobs(ctx context.Context) {
	cronJobs := &batchv1.CronJobList{}
	if err := w.cache.List(ctx, cronJobs, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list cronjobs during scan")
		return
	}

	for i := range cronJobs.Items {
		cj := &cronJobs.Items[i]
		if !w.shouldWatchNamespace(cj.Namespace) {
			continue
		}
		w.checkCronJobFailures(ctx, cj)
	}
}

// checkCronJobFailures looks up child Jobs owned by the CronJob and checks for failures.
func (w *CronJobWatcher) checkCronJobFailures(ctx context.Context, cj *batchv1.CronJob) {
	jobs := &batchv1.JobList{}
	if err := w.cache.List(ctx, jobs, &client.ListOptions{Namespace: cj.Namespace}); err != nil {
		w.log.Error(err, "Failed to list jobs for cronjob", "cronjob", cj.Name)
		return
	}

	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !isOwnedByCronJob(job, cj.UID) {
			continue
		}

		cond, ok := jobFailedCondition(job)
		if !ok {
			continue
		}

		if !w.markFailAlerted(job.UID) {
			continue
		}

		at := cond.LastTransitionTime.Time
		if at.IsZero() {
			at = w.clock()
		}

		w.emitter.Emit(CronJobFailedEvent{
			BaseEvent: BaseEvent{
				At:        at,
				AgentName: w.config.AgentName,
				Namespace: cj.Namespace,
				PodName:   cj.Name,
			},
			CronJobName: cj.Name,
			LastJobName: job.Name,
			Reason:      cond.Reason,
			Message:     fmt.Sprintf("CronJob %s/%s child job %s failed: %s", cj.Namespace, cj.Name, job.Name, cond.Message),
		})
	}
}

func (w *CronJobWatcher) markFailAlerted(uid types.UID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.failAlerted[uid]; ok {
		return false
	}
	w.failAlerted[uid] = struct{}{}
	return true
}

func (w *CronJobWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

// isOwnedByCronJob checks if a Job is owned by the given CronJob UID.
func isOwnedByCronJob(job *batchv1.Job, cronJobUID types.UID) bool {
	for _, ref := range job.OwnerReferences {
		if ref.UID == cronJobUID {
			return true
		}
	}
	return false
}
