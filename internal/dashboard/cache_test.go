package dashboard

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJSONCache_FetchCachesWithinTTL(t *testing.T) {
	c := newJSONCache(time.Second)
	var calls int32
	fn := func() (any, error) {
		atomic.AddInt32(&calls, 1)
		return map[string]string{"hello": "world"}, nil
	}
	for i := range 5 {
		body, etag, err := c.Fetch("k1", fn)
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		if etag == "" {
			t.Errorf("etag should be set")
		}
		if len(body) == 0 {
			t.Errorf("body empty")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times, want 1 (cached within TTL)", got)
	}
}

func TestJSONCache_FetchExpiresAfterTTL(t *testing.T) {
	c := newJSONCache(20 * time.Millisecond)
	var calls int32
	fn := func() (any, error) { atomic.AddInt32(&calls, 1); return 1, nil }
	if _, _, err := c.Fetch("k", fn); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, _, err := c.Fetch("k", fn); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fn called %d times, want 2 (TTL expired)", got)
	}
}

func TestJSONCache_FetchWithTTLOverridesDefault(t *testing.T) {
	c := newJSONCache(time.Hour) // default very long
	var calls int32
	fn := func() (any, error) { atomic.AddInt32(&calls, 1); return 1, nil }
	// Custom 20ms TTL — second fetch after sleep should re-build.
	if _, _, err := c.FetchWithTTL("k", 20*time.Millisecond, fn); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, _, err := c.FetchWithTTL("k", 20*time.Millisecond, fn); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fn called %d times under short TTL, want 2", got)
	}
}

func TestJSONCache_Invalidate_ForcesRebuild(t *testing.T) {
	c := newJSONCache(time.Hour)
	var calls int32
	fn := func() (any, error) { atomic.AddInt32(&calls, 1); return 1, nil }
	if _, _, err := c.Fetch("k", fn); err != nil {
		t.Fatal(err)
	}
	c.Invalidate("k")
	if _, _, err := c.Fetch("k", fn); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fn called %d times after Invalidate, want 2", got)
	}
}

// TestJSONCache_SingleflightCoalescesConcurrent verifies that N concurrent
// callers for the same uncached key share a single fn invocation.
func TestJSONCache_SingleflightCoalescesConcurrent(t *testing.T) {
	c := newJSONCache(time.Hour)
	var calls int32
	start := make(chan struct{})
	// Returning (any, error) is required by the cache.Fetch signature even
	// though this fn always succeeds; the linter's nil-error nag is a
	// false-positive against an externally-imposed shape.
	fn := func() (any, error) { //nolint:unparam
		atomic.AddInt32(&calls, 1)
		<-start
		return map[string]int{"v": 1}, nil
	}

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, _, err := c.Fetch("k", fn); err != nil {
				t.Errorf("fetch: %v", err)
			}
		}()
	}
	// Give goroutines a chance to enter Fetch.
	time.Sleep(20 * time.Millisecond)
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times for %d concurrent callers, want 1 (singleflight)", got, N)
	}
}

func TestJSONCache_FetchPropagatesError(t *testing.T) {
	c := newJSONCache(time.Hour)
	want := "explode"
	_, _, err := c.Fetch("k", func() (any, error) { return nil, errString(want) })
	if err == nil || err.Error() != want {
		t.Errorf("error not propagated: got %v", err)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestJSONCache_NilCacheDegradesToDirectBuild(t *testing.T) {
	var c *jsonCache
	body, etag, err := c.Fetch("k", func() (any, error) { return 42, nil })
	if err != nil {
		t.Fatalf("nil cache should still build: %v", err)
	}
	if len(body) == 0 || etag == "" {
		t.Errorf("body/etag should be produced even without cache")
	}
}

// --- writeCachedJSON --------------------------------------------------------

func TestWriteCachedJSON_HonoursIfNoneMatch(t *testing.T) {
	body := []byte(`{"x":1}`)
	etag := `"abc"`

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("If-None-Match", etag)
	writeCachedJSON(rr, req, body, etag)
	if rr.Code != http.StatusNotModified {
		t.Errorf("matching ETag should produce 304, got %d", rr.Code)
	}
}

func TestWriteCachedJSON_WritesBodyAndEtagOnMiss(t *testing.T) {
	body := []byte(`{"x":1}`)
	etag := `"abc"`
	rr := httptest.NewRecorder()
	writeCachedJSON(rr, httptest.NewRequest(http.MethodGet, "/api/x", nil), body, etag)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("ETag") != etag {
		t.Errorf("ETag header missing")
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestWriteCachedJSON_AppendsTrailingNewline(t *testing.T) {
	body := []byte(`{"x":1}`)
	rr := httptest.NewRecorder()
	writeCachedJSON(rr, httptest.NewRequest(http.MethodGet, "/api/x", nil), body, `"e"`)
	got := rr.Body.Bytes()
	if got[len(got)-1] != '\n' {
		t.Errorf("response should end with newline, got %q", got)
	}
}
