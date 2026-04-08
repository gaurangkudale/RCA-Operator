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

package topology

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// Cache provides a TTL-based in-memory cache for the ServiceGraph.
// It periodically refreshes the graph in the background and serves
// cached results to dashboard requests within the TTL window.
type Cache struct {
	builder          *Builder
	ttl              time.Duration
	dependencyWindow time.Duration

	mu    sync.RWMutex
	graph *ServiceGraph
	age   time.Time

	// getIncidents is a callback to fetch current incident state for graph enrichment.
	getIncidents func() []IncidentRef

	log logr.Logger
}

// CacheOption configures the topology cache.
type CacheOption func(*Cache)

// WithTTL sets the cache TTL. Default is 30 seconds.
func WithTTL(ttl time.Duration) CacheOption {
	return func(c *Cache) { c.ttl = ttl }
}

// WithDependencyWindow sets the lookback window for dependency queries. Default is 15 minutes.
func WithDependencyWindow(window time.Duration) CacheOption {
	return func(c *Cache) { c.dependencyWindow = window }
}

// WithIncidentsFn sets the callback used to fetch current incidents for graph enrichment.
func WithIncidentsFn(fn func() []IncidentRef) CacheOption {
	return func(c *Cache) { c.getIncidents = fn }
}

// NewCache creates a topology cache that wraps the given builder.
func NewCache(builder *Builder, log logr.Logger, opts ...CacheOption) *Cache {
	c := &Cache{
		builder:          builder,
		ttl:              30 * time.Second,
		dependencyWindow: 15 * time.Minute,
		log:              log,
		getIncidents:     func() []IncidentRef { return nil },
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the cached ServiceGraph, rebuilding it if the cache has expired.
func (c *Cache) Get(ctx context.Context) (*ServiceGraph, error) {
	c.mu.RLock()
	if c.graph != nil && time.Since(c.age) < c.ttl {
		g := c.graph
		c.mu.RUnlock()
		return g, nil
	}
	c.mu.RUnlock()

	return c.Refresh(ctx)
}

// Refresh forces a rebuild of the topology graph from the telemetry backend.
func (c *Cache) Refresh(ctx context.Context) (*ServiceGraph, error) {
	incidents := c.getIncidents()

	graph, err := c.builder.BuildGraph(ctx, c.dependencyWindow, incidents)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.graph = graph
	c.age = time.Now()
	c.mu.Unlock()

	return graph, nil
}

// StartBackgroundRefresh starts a goroutine that refreshes the cache at the TTL interval.
// The goroutine stops when the context is cancelled.
func (c *Cache) StartBackgroundRefresh(ctx context.Context) {
	ticker := time.NewTicker(c.ttl)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := c.Refresh(ctx); err != nil {
					c.log.V(1).Info("background topology refresh failed", "error", err)
				}
			}
		}
	}()
}
