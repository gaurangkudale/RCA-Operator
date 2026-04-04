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
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultJobScanInterval = 30 * time.Second
)

// JobWatcherConfig controls the behaviour of the Job failure watcher.
type JobWatcherConfig struct {
	AgentName       string
	WatchNamespaces []string
	ScanInterval    time.Duration
}

// JobWatcher monitors batch/v1 Jobs and emits a JobFailedEvent when a Job reaches
// a Failed condition (typically BackoffLimitExceeded or DeadlineExceeded).
//
// At most one event is emitted per (Job UID, observedGeneration) pair.
type JobWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  JobWatcherConfig
	clock   func() time.Time

	mu           sync.Mutex
	failAlerted  map[types.UID]struct{}
	namespaceSet map[string]struct{}
}

// NewJobWatcher creates a JobWatcher backed by a controller-runtime cache.
func NewJobWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg JobWatcherConfig) *JobWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultJobScanInterval
	}

	return &JobWatcher{
		cache:        cache,
		emitter:      emitter,
		log:          logger.WithName("job-watcher"),
		config:       cfg,
		clock:        time.Now,
		failAlerted:  make(map[types.UID]struct{}),
		namespaceSet: toNamespaceSet(cfg.WatchNamespaces),
	}
}

// Start registers informer handlers, runs a bootstrap scan, and launches the
// periodic fallback scanner. Non-blocking; all goroutines are bounded by ctx.
func (w *JobWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &batchv1.Job{})
	if err != nil {
		return fmt.Errorf("failed to get job informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			job, ok := toJob(obj)
			if !ok {
				return
			}
			w.onAdd(job)
		},
		UpdateFunc: func(_, newObj any) {
			job, ok := toJob(newObj)
			if !ok {
				return
			}
			w.onUpdate(job)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add job informer handler: %w", err)
	}

	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("Job watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanJobs, w.config.ScanInterval)

	w.log.Info("Started job watcher", "scanInterval", w.config.ScanInterval.String())
	return nil
}

func (w *JobWatcher) onAdd(job *batchv1.Job) {
	if !w.shouldWatchNamespace(job.Namespace) {
		return
	}
	w.detectFailed(job)
}

func (w *JobWatcher) onUpdate(job *batchv1.Job) {
	if !w.shouldWatchNamespace(job.Namespace) {
		return
	}
	w.detectFailed(job)
}

// detectFailed checks whether a Job has reached a Failed condition.
func (w *JobWatcher) detectFailed(job *batchv1.Job) {
	cond, ok := jobFailedCondition(job)
	if !ok {
		return
	}

	if !w.markFailAlerted(job.UID) {
		return
	}

	at := cond.LastTransitionTime.Time
	if at.IsZero() {
		at = w.clock()
	}

	w.emitter.Emit(JobFailedEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: job.Namespace,
			PodName:   job.Name,
		},
		JobName: job.Name,
		Reason:  cond.Reason,
		Message: cond.Message,
	})
}

func (w *JobWatcher) bootstrapScan(ctx context.Context) {
	list := &batchv1.JobList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list jobs for bootstrap scan")
		return
	}

	scanned := 0
	for i := range list.Items {
		job := &list.Items[i]
		if !w.shouldWatchNamespace(job.Namespace) {
			continue
		}
		w.detectFailed(job)
		scanned++
	}
	w.log.Info("Job watcher bootstrap scan complete", "scanned", scanned)
}

func (w *JobWatcher) scanJobs(ctx context.Context) {
	list := &batchv1.JobList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list jobs during periodic scan")
		return
	}

	for i := range list.Items {
		job := &list.Items[i]
		if !w.shouldWatchNamespace(job.Namespace) {
			continue
		}
		w.detectFailed(job)
	}
}

func (w *JobWatcher) markFailAlerted(uid types.UID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.failAlerted[uid]; ok {
		return false
	}
	w.failAlerted[uid] = struct{}{}
	return true
}

func (w *JobWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

func toJob(obj any) (*batchv1.Job, bool) {
	switch t := obj.(type) {
	case *batchv1.Job:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		job, ok := t.Obj.(*batchv1.Job)
		return job, ok
	default:
		return nil, false
	}
}

// jobFailedCondition returns the Failed condition from the Job's status if it is True.
func jobFailedCondition(job *batchv1.Job) (batchv1.JobCondition, bool) {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == "True" {
			return cond, true
		}
	}
	return batchv1.JobCondition{}, false
}
