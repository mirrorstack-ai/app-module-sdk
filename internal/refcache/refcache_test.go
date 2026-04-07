package refcache

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeResource is a stand-in for *pgxpool.Pool / *cache.Client.
type fakeResource struct {
	id      int
	closed  atomic.Bool
	closeFn func()
}

func newFakeCache(maxSize int) *Cache[*fakeResource] {
	return New[*fakeResource](maxSize, "test-cache:", func(r *fakeResource) {
		r.closed.Store(true)
		if r.closeFn != nil {
			r.closeFn()
		}
	})
}

func TestGet_FactoryCalledOnMiss(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	calls := 0
	value, release, err := cache.Get("k", func() (*fakeResource, error) {
		calls++
		return &fakeResource{id: 42}, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer release()

	if calls != 1 {
		t.Errorf("factory called %d times, want 1", calls)
	}
	if value.id != 42 {
		t.Errorf("value.id = %d, want 42", value.id)
	}
}

func TestGet_FactoryCachedOnHit(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	calls := 0
	factory := func() (*fakeResource, error) {
		calls++
		return &fakeResource{id: calls}, nil
	}

	v1, r1, err := cache.Get("k", factory)
	if err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	defer r1()

	v2, r2, err := cache.Get("k", factory)
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	defer r2()

	if calls != 1 {
		t.Errorf("factory called %d times, want 1 (second call should hit cache)", calls)
	}
	if v1 != v2 {
		t.Error("expected the same value pointer on cache hit")
	}
}

func TestGet_FactoryError(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	wantErr := errors.New("factory boom")
	_, _, err := cache.Get("k", func() (*fakeResource, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestRelease_DecrementsRefcount(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	_, r1, _ := cache.Get("k", func() (*fakeResource, error) { return &fakeResource{}, nil })
	_, r2, _ := cache.Get("k", func() (*fakeResource, error) { return &fakeResource{}, nil })
	_, r3, _ := cache.Get("k", func() (*fakeResource, error) { return &fakeResource{}, nil })

	cache.mu.Lock()
	got := cache.items["k"].refCount
	cache.mu.Unlock()
	if got != 3 {
		t.Errorf("refCount after 3 Gets = %d, want 3", got)
	}

	r1()
	r2()

	cache.mu.Lock()
	got = cache.items["k"].refCount
	cache.mu.Unlock()
	if got != 1 {
		t.Errorf("refCount after 2 releases = %d, want 1", got)
	}

	// Underflow protection: extra releases past zero must not go negative.
	r3()
	r3()
	r3()

	cache.mu.Lock()
	got = cache.items["k"].refCount
	cache.mu.Unlock()
	if got != 0 {
		t.Errorf("refCount = %d, want 0 (no underflow)", got)
	}
}

func TestEviction_SkipsPinnedEntries(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(2)

	// Fill cache with two entries, both refcount=1 (pinned).
	_, r1, _ := cache.Get("a", func() (*fakeResource, error) { return &fakeResource{id: 1}, nil })
	_, _, _ = cache.Get("b", func() (*fakeResource, error) { return &fakeResource{id: 2}, nil })

	// Release "a" so it becomes evictable, but "b" stays pinned.
	r1()

	// Adding "c" must evict "a" (the only unpinned entry), not "b".
	_, _, err := cache.Get("c", func() (*fakeResource, error) { return &fakeResource{id: 3}, nil })
	if err != nil {
		t.Fatalf("Get c: %v", err)
	}

	cache.mu.Lock()
	_, hasA := cache.items["a"]
	_, hasB := cache.items["b"]
	_, hasC := cache.items["c"]
	cache.mu.Unlock()

	if hasA {
		t.Error("a should have been evicted (was unpinned)")
	}
	if !hasB {
		t.Error("b should still exist (pinned, refCount > 0)")
	}
	if !hasC {
		t.Error("c should have been inserted")
	}
}

func TestEviction_AllPinned_ReturnsLimitError(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(2)
	_, _, _ = cache.Get("a", func() (*fakeResource, error) { return &fakeResource{}, nil })
	_, _, _ = cache.Get("b", func() (*fakeResource, error) { return &fakeResource{}, nil })

	// Both entries are pinned. Adding a third must fail with a limit error.
	_, _, err := cache.Get("c", func() (*fakeResource, error) { return &fakeResource{}, nil })
	if err == nil {
		t.Fatal("expected limit error when all entries pinned")
	}
}

// TestSlowPath_SingleflightCoalesces verifies that concurrent Gets for the
// same missing key coalesce via singleflight: exactly ONE factory call runs,
// all callers receive the same value, and each caller gets its own refcount.
//
// Determinism: each caller increments a counter on entry to Get and spins
// until all N have entered. Because the fast-path miss happens before the
// caller's entry-counter increment, we know all N goroutines have passed
// the cache-empty check by the time the barrier releases. They then all
// invoke sf.Do, which serializes them — only the leader's factory runs.
//
// A 2-second deadline prevents infinite spin if the timing assumption breaks.
func TestSlowPath_SingleflightCoalesces(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	defer cache.Close()

	const goroutines = 8

	var createdMu sync.Mutex
	var allResources []*fakeResource
	var entered atomic.Int32

	factory := func() (*fakeResource, error) {
		r := &fakeResource{}
		createdMu.Lock()
		allResources = append(allResources, r)
		createdMu.Unlock()

		// Hold the factory open until all N callers have entered Get,
		// guaranteeing all N hit the slow path before any of them inserts.
		deadline := time.Now().Add(2 * time.Second)
		for entered.Load() < goroutines && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		return r, nil
	}

	results := make(chan *fakeResource, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered.Add(1)
			v, release, err := cache.Get("k", factory)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			defer release()
			results <- v
		}()
	}
	wg.Wait()
	close(results)

	// All callers must receive the SAME value (singleflight guarantees this).
	var canonical *fakeResource
	count := 0
	for v := range results {
		count++
		if canonical == nil {
			canonical = v
			continue
		}
		if v != canonical {
			t.Errorf("got value %p, want canonical %p (all callers must see the same entry)", v, canonical)
		}
	}
	if count != goroutines {
		t.Fatalf("only %d goroutines returned a value, want %d", count, goroutines)
	}
	if canonical == nil {
		t.Fatal("no goroutine received a value")
	}

	createdMu.Lock()
	resources := append([]*fakeResource(nil), allResources...)
	createdMu.Unlock()

	// SINGLEFLIGHT INVARIANT: factory must run exactly once for N concurrent
	// Gets on the same missing key, regardless of how many goroutines raced.
	if len(resources) != 1 {
		t.Errorf("factory ran %d times, want exactly 1 (singleflight should coalesce)", len(resources))
	}
	if resources[0] != canonical {
		t.Error("the single created resource is not the one callers received")
	}
	// The canonical resource must NOT be closed — all N callers should now
	// have released, total refcount = 0, but no eviction was triggered.
	if canonical.closed.Load() {
		t.Error("canonical resource was closed despite still being cached")
	}
}

func TestClose_DisposesAllEntries(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	resources := []*fakeResource{{id: 1}, {id: 2}, {id: 3}}
	for i, r := range resources {
		key := strconv.Itoa(i)
		v := r
		_, _, _ = cache.Get(key, func() (*fakeResource, error) { return v, nil })
	}

	cache.Close()

	for _, r := range resources {
		if !r.closed.Load() {
			t.Errorf("resource %d was not closed", r.id)
		}
	}
}

func TestClose_ClearsMap(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	_, _, _ = cache.Get("k", func() (*fakeResource, error) { return &fakeResource{}, nil })
	cache.Close()

	cache.mu.Lock()
	n := len(cache.items)
	cache.mu.Unlock()
	if n != 0 {
		t.Errorf("items map size = %d after Close, want 0", n)
	}
}

func TestGet_FactoryNotCalledOnPreFilledHit(t *testing.T) {
	t.Parallel()

	cache := newFakeCache(10)
	cache.items["k"] = &entry[*fakeResource]{
		value:    &fakeResource{id: 99},
		lastUsed: time.Now(),
		refCount: 0,
	}

	called := false
	v, release, err := cache.Get("k", func() (*fakeResource, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer release()

	if called {
		t.Error("factory should not be called on cache hit")
	}
	if v.id != 99 {
		t.Errorf("v.id = %d, want 99", v.id)
	}
}

// TestLabelInError ensures the limit-error message includes the configured
// label so callers can attribute pool-limit errors to a specific domain.
func TestLabelInError(t *testing.T) {
	t.Parallel()

	cache := New[*fakeResource](1, "test-domain:", func(r *fakeResource) { r.closed.Store(true) })
	_, _, _ = cache.Get("a", func() (*fakeResource, error) { return &fakeResource{}, nil })
	_, _, err := cache.Get("b", func() (*fakeResource, error) { return &fakeResource{}, nil })
	if err == nil {
		t.Fatal("expected limit error")
	}
	if !strings.Contains(err.Error(), "test-domain:") {
		t.Errorf("error %q does not contain label %q", err, "test-domain:")
	}
}
