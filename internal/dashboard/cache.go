package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// jsonCacheEntry holds a pre-serialized JSON body plus the ETag we advertise
// for it. We cache the serialized form so repeat handlers don't re-marshal.
type jsonCacheEntry struct {
	body []byte
	etag string
	at   time.Time
}

// jsonCache provides a TTL-bounded, single-flight JSON response cache keyed by
// an endpoint-local string (e.g. "topology?view=summary"). It is safe for
// concurrent use: overlapping callers for the same key share one build.
type jsonCache struct {
	mu       sync.Mutex
	entries  map[string]*jsonCacheEntry
	inflight map[string]*buildCall
	ttl      time.Duration
}

type buildCall struct {
	done chan struct{}
	body []byte
	etag string
	err  error
}

func newJSONCache(ttl time.Duration) *jsonCache {
	return &jsonCache{
		entries:  make(map[string]*jsonCacheEntry),
		inflight: make(map[string]*buildCall),
		ttl:      ttl,
	}
}

// Fetch is FetchWithTTL using the cache's default TTL.
func (c *jsonCache) Fetch(key string, fn func() (any, error)) ([]byte, string, error) {
	var ttl time.Duration
	if c != nil {
		ttl = c.ttl
	}
	return c.FetchWithTTL(key, ttl, fn)
}

// FetchWithTTL returns (body, etag) for key, building it via fn if missing or
// older than ttl. Concurrent calls for the same key coalesce onto a single
// build. Use this for entries whose freshness expectations differ from the
// cache default (e.g. immutable trace data that should cache for minutes).
// When c is nil, FetchWithTTL degrades to a direct uncached build.
func (c *jsonCache) FetchWithTTL(key string, ttl time.Duration, fn func() (any, error)) ([]byte, string, error) {
	if c == nil {
		val, err := fn()
		if err != nil {
			return nil, "", err
		}
		body, err := json.Marshal(val)
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(body)
		return body, `"` + hex.EncodeToString(sum[:16]) + `"`, nil
	}
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Since(e.at) < ttl {
		body, etag := e.body, e.etag
		c.mu.Unlock()
		return body, etag, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.body, call.etag, call.err
	}
	call := &buildCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	val, err := fn()
	if err == nil {
		var body []byte
		body, err = json.Marshal(val)
		if err == nil {
			sum := sha256.Sum256(body)
			call.body = body
			call.etag = `"` + hex.EncodeToString(sum[:16]) + `"`
		}
	}
	call.err = err

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.entries[key] = &jsonCacheEntry{body: call.body, etag: call.etag, at: time.Now()}
	}
	c.mu.Unlock()
	close(call.done)
	return call.body, call.etag, err
}

// Invalidate drops a key so the next Fetch rebuilds.
func (c *jsonCache) Invalidate(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// writeCachedJSON serves a cached JSON body with ETag + Cache-Control headers,
// honouring If-None-Match to return 304. Callers supply body/etag obtained
// from jsonCache.Fetch.
func writeCachedJSON(w http.ResponseWriter, r *http.Request, body []byte, etag string) {
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if secs := int(defaultCacheTTL / time.Second); secs > 0 {
		w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(secs))
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	if etag != "" && r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = w.Write([]byte{'\n'})
	}
}
