package server

import (
	"sync"
	"time"
)

// respCache memoizes the JSON of an expensive read endpoint for a short TTL and
// collapses concurrent misses into a single computation (ADR-0028).
//
// The dashboard polls the two heaviest endpoints — overview and live — from
// several places at once (the page on an interval, the menubar bar, extra
// tabs). Each request re-ran overview's ~18 store queries and rebuilt the live
// payload; measured on the hub, four concurrent overviews took 9× a single one
// as they fought the CPU. A monitoring view tolerates a few seconds of
// staleness, so serving one recent computation to everyone removes the pile-up
// without changing what the numbers mean.
type respCache struct {
	mu  sync.Mutex
	m   map[string]*cacheEntry
	now func() time.Time
}

type cacheEntry struct {
	body    []byte
	err     error
	expires time.Time
	// ready is non-nil while a computation is in flight; waiters block on it and
	// then read body/err (the channel close publishes those writes).
	ready chan struct{}
}

func newRespCache(now func() time.Time) *respCache {
	return &respCache{m: make(map[string]*cacheEntry), now: now}
}

// cachedJSON serves compute through the response cache, or runs it directly when
// no cache is configured. Only New sets up the cache; the many tests that build
// a Server literal have no cache and get uncached, always-fresh computation —
// which is what those assertions expect.
func (s *Server) cachedJSON(key string, ttl time.Duration, compute func() ([]byte, error)) ([]byte, error) {
	if s.readCache == nil {
		return compute()
	}
	return s.readCache.bytes(key, ttl, compute)
}

// bytes returns the cached response for key when it is younger than ttl. On a
// miss or expiry exactly one caller runs compute while the rest wait and share
// its result. Failures are never cached — a transient error must not stick for
// the whole ttl — so the next request after one retries.
func (c *respCache) bytes(key string, ttl time.Duration, compute func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e := c.m[key]; e != nil {
		if e.ready != nil {
			ch := e.ready
			c.mu.Unlock()
			<-ch
			return e.body, e.err
		}
		if c.now().Before(e.expires) {
			body := e.body
			c.mu.Unlock()
			return body, nil
		}
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.m[key] = e
	c.mu.Unlock()

	body, err := compute()

	c.mu.Lock()
	e.body, e.err = body, err
	if err != nil {
		delete(c.m, key)
	} else {
		e.expires = c.now().Add(ttl)
	}
	ch := e.ready
	e.ready = nil
	c.mu.Unlock()
	close(ch) // wakes any waiters, which then read body/err

	return body, err
}
