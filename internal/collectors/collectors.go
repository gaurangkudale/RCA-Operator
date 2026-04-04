package collectors

import (
	"github.com/go-logr/logr"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// Signal is the temporary compatibility bridge used while the runtime is moved
// from watcher-specific events to a normalized signal contract.
type Signal = watcher.CorrelatorEvent

// SignalEmitter delivers collected signals to the incident engine.
type SignalEmitter = watcher.EventEmitter

type PodCollectorConfig = watcher.PodWatcherConfig
type EventCollectorConfig = watcher.EventWatcherConfig
type WorkloadCollectorConfig = watcher.DeploymentWatcherConfig
type NodeCollectorConfig = watcher.NodeWatcherConfig
type StatefulSetCollectorConfig = watcher.StatefulSetWatcherConfig
type DaemonSetCollectorConfig = watcher.DaemonSetWatcherConfig
type JobCollectorConfig = watcher.JobWatcherConfig
type CronJobCollectorConfig = watcher.CronJobWatcherConfig

func NewChannelSignalEmitter(ch chan<- Signal, logger logr.Logger) SignalEmitter {
	return watcher.NewChannelEventEmitter(ch, logger)
}

func NewPodCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg PodCollectorConfig,
) *watcher.PodWatcher {
	return watcher.NewPodWatcher(cache, emitter, logger, cfg)
}

func NewEventCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg EventCollectorConfig,
) *watcher.EventWatcher {
	return watcher.NewEventWatcher(cache, emitter, logger, cfg)
}

// NewWorkloadCollector currently wraps Deployment rollout collection. This is
// the refactor seam where additional workload collectors can be introduced.
func NewWorkloadCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg WorkloadCollectorConfig,
) *watcher.DeploymentWatcher {
	return watcher.NewDeploymentWatcher(cache, emitter, logger, cfg)
}

func NewNodeCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg NodeCollectorConfig,
) *watcher.NodeWatcher {
	return watcher.NewNodeWatcher(cache, emitter, logger, cfg)
}

func NewStatefulSetCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg StatefulSetCollectorConfig,
) *watcher.StatefulSetWatcher {
	return watcher.NewStatefulSetWatcher(cache, emitter, logger, cfg)
}

func NewDaemonSetCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg DaemonSetCollectorConfig,
) *watcher.DaemonSetWatcher {
	return watcher.NewDaemonSetWatcher(cache, emitter, logger, cfg)
}

func NewJobCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg JobCollectorConfig,
) *watcher.JobWatcher {
	return watcher.NewJobWatcher(cache, emitter, logger, cfg)
}

func NewCronJobCollector(
	cache ctrlcache.Cache,
	emitter SignalEmitter,
	logger logr.Logger,
	cfg CronJobCollectorConfig,
) *watcher.CronJobWatcher {
	return watcher.NewCronJobWatcher(cache, emitter, logger, cfg)
}
