package signals

import (
	"sync"
	"time"
)

// Deduplicator suppresses duplicate signals within a configurable time window.
type Deduplicator struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	window  time.Duration
	nowFn   func() time.Time
}

// NewDeduplicator creates a Deduplicator with the given window duration.
func NewDeduplicator(window time.Duration) *Deduplicator {
	return &Deduplicator{
		seen:   make(map[string]time.Time),
		window: window,
		nowFn:  time.Now,
	}
}

// IsDuplicate returns true if the given dedup key was already seen within the window.
// If not a duplicate, the key is recorded with the current timestamp.
func (d *Deduplicator) IsDuplicate(key string) bool {
	now := d.nowFn()
	d.mu.Lock()
	defer d.mu.Unlock()

	d.purgeOld(now)

	if lastSeen, exists := d.seen[key]; exists {
		if now.Sub(lastSeen) < d.window {
			return true
		}
	}
	d.seen[key] = now
	return false
}

func (d *Deduplicator) purgeOld(now time.Time) {
	cutoff := now.Add(-d.window)
	for key, ts := range d.seen {
		if ts.Before(cutoff) {
			delete(d.seen, key)
		}
	}
}
