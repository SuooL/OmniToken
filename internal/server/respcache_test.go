package server

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRespCacheServesWithinTTLAndRecomputesAfter(t *testing.T) {
	clock := time.UnixMilli(0)
	c := newRespCache(func() time.Time { return clock })
	var calls atomic.Int64
	compute := func() ([]byte, error) {
		calls.Add(1)
		return []byte("v"), nil
	}

	if b, _ := c.bytes("k", 10*time.Second, compute); string(b) != "v" {
		t.Fatalf("first = %q", b)
	}
	// Still within the TTL: served from cache, compute not called again.
	clock = time.UnixMilli(9_000)
	if _, _ = c.bytes("k", 10*time.Second, compute); calls.Load() != 1 {
		t.Fatalf("compute ran %d times within ttl, want 1", calls.Load())
	}
	// Past the TTL: recomputed.
	clock = time.UnixMilli(11_000)
	if _, _ = c.bytes("k", 10*time.Second, compute); calls.Load() != 2 {
		t.Fatalf("compute ran %d times after ttl, want 2", calls.Load())
	}
}

func TestRespCacheDoesNotCacheErrors(t *testing.T) {
	clock := time.UnixMilli(0)
	c := newRespCache(func() time.Time { return clock })
	var calls atomic.Int64
	compute := func() ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}
	if _, err := c.bytes("k", time.Hour, compute); err == nil {
		t.Fatal("want error")
	}
	// A failure must not be cached: the next call retries even well within ttl.
	if _, err := c.bytes("k", time.Hour, compute); err == nil {
		t.Fatal("want error on retry")
	}
	if calls.Load() != 2 {
		t.Fatalf("compute ran %d times, want 2 (errors not cached)", calls.Load())
	}
}

func TestRespCacheCollapsesConcurrentMisses(t *testing.T) {
	clock := time.UnixMilli(0)
	c := newRespCache(func() time.Time { return clock })
	var calls atomic.Int64
	release := make(chan struct{})
	compute := func() ([]byte, error) {
		calls.Add(1)
		<-release // hold the single flight open until every caller has arrived
		return []byte("shared"), nil
	}

	const n = 20
	var wg sync.WaitGroup
	got := make([][]byte, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, _ := c.bytes("k", time.Hour, compute)
			got[i] = b
		}(i)
	}
	// Give the goroutines time to pile up on the in-flight entry, then let it go.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("compute ran %d times, want 1 (single-flight)", calls.Load())
	}
	for i, b := range got {
		if string(b) != "shared" {
			t.Fatalf("caller %d got %q, want shared", i, b)
		}
	}
}
