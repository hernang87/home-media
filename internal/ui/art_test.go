package ui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

const artWait = 5 * time.Second

// fyne.Do needs a current app; the test driver runs its callbacks synchronously.
var testAppOnce sync.Once

func newTestApp() {
	testAppOnce.Do(func() { test.NewApp() })
}

func recvWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(artWait):
		t.Fatal("timed out waiting for value")
	}
	var zero T
	return zero
}

// waitInflightClear polls until the cache has no download running.
func waitInflightClear(t *testing.T, c *ArtCache) {
	t.Helper()
	deadline := time.After(artWait)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		c.mu.Lock()
		n := len(c.inflight)
		c.mu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("download still in flight")
		}
	}
}

func TestArtCacheCoalescesInflight(t *testing.T) {
	newTestApp()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	c := NewArtCache(4, func(ctx context.Context, rel *string) ([]byte, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return []byte("img"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	key := "a.jpg"
	got := make(chan string, 2)
	c.Get(&key, func(r fyne.Resource) { got <- r.Name() })
	recvWithin(t, started)
	c.Get(&key, func(r fyne.Resource) { got <- r.Name() })
	close(release)

	for i := 0; i < 2; i++ {
		if name := recvWithin(t, got); name != key {
			t.Fatalf("callback %d got resource %q, want %q", i, name, key)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fetch called %d times, want 1", n)
	}
}

func TestArtCacheCachedHitIsImmediate(t *testing.T) {
	newTestApp()
	var calls atomic.Int32
	c := NewArtCache(4, func(ctx context.Context, rel *string) ([]byte, error) {
		calls.Add(1)
		return []byte(*rel), nil
	})
	key := "b.jpg"
	got := make(chan string, 1)
	c.Get(&key, func(r fyne.Resource) { got <- r.Name() })
	recvWithin(t, got)

	// A cache hit calls cb before Get returns, so no waiting is needed.
	var hit bool
	var name string
	c.Get(&key, func(r fyne.Resource) { hit = true; name = r.Name() })
	if !hit || name != key {
		t.Fatalf("cached Get: cb called=%v name=%q, want immediate call with %q", hit, name, key)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fetch called %d times, want 1", n)
	}
}

func TestArtCacheEvictsLeastRecentlyUsed(t *testing.T) {
	newTestApp()
	var calls atomic.Int32
	c := NewArtCache(2, func(ctx context.Context, rel *string) ([]byte, error) {
		calls.Add(1)
		return []byte(*rel), nil
	})
	got := make(chan string, 1)
	get := func(k string) {
		t.Helper()
		c.Get(&k, func(r fyne.Resource) { got <- r.Name() })
		recvWithin(t, got)
	}
	expectCalls := func(want int32) {
		t.Helper()
		if n := calls.Load(); n != want {
			t.Fatalf("fetch called %d times, want %d", n, want)
		}
	}

	get("a")
	get("b")
	get("c") // cache now holds c and b; a is the least recently used
	expectCalls(3)

	get("b") // cached, no fetch
	expectCalls(3)

	get("a") // evicted, so it must be fetched again
	expectCalls(4)
}

func TestArtCacheIgnoresEmptyRel(t *testing.T) {
	newTestApp()
	var calls atomic.Int32
	c := NewArtCache(2, func(ctx context.Context, rel *string) ([]byte, error) {
		calls.Add(1)
		return []byte("x"), nil
	})
	empty := ""
	var cbCalled bool
	cb := func(fyne.Resource) { cbCalled = true }
	c.Get(nil, cb)
	c.Get(&empty, cb)
	if cbCalled {
		t.Fatal("cb called for nil or empty rel")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("fetch called %d times for nil or empty rel, want 0", n)
	}
}

func TestArtCacheFailedFetchIsRetried(t *testing.T) {
	newTestApp()
	var calls atomic.Int32
	c := NewArtCache(2, func(ctx context.Context, rel *string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		return []byte(*rel), nil
	})
	key := "c.jpg"
	var failedCbCalled atomic.Bool
	c.Get(&key, func(fyne.Resource) { failedCbCalled.Store(true) })
	waitInflightClear(t, c)
	if failedCbCalled.Load() {
		t.Fatal("cb called after failed fetch")
	}

	got := make(chan string, 1)
	c.Get(&key, func(r fyne.Resource) { got <- r.Name() })
	if name := recvWithin(t, got); name != key {
		t.Fatalf("retry got resource %q, want %q", name, key)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("fetch called %d times, want 2", n)
	}
}
