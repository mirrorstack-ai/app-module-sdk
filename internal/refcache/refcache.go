// Package refcache implements a generic refcount-pinned LRU cache for
// expensive credential-keyed resources (DB pools, Redis clients).
//
// Two design properties matter:
//
//   - Refcount-pinned entries: Get() returns the resource plus a release
//     closure. The entry's refcount is held above zero until release runs, so
//     concurrent eviction cannot close a resource another goroutine is using.
//
//   - Singleflight on the slow path: when N goroutines miss the fast path for
//     the same key, only ONE factory call runs (the leader's). The other N-1
//     wait for it to complete, then bump the refcount on the leader's inserted
//     entry. This eliminates wasted parallel TLS handshakes / pool creations
//     under contention, which is especially important for pgx and Redis where
//     each connection costs 50-200ms of network I/O.
//
// db.PoolCache and cache.ClientCache are thin wrappers around this package —
// they add credential validation, key derivation, and resource construction,
// then delegate the lifecycle management here.
package refcache

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Cache is a refcount-pinned LRU cache of T values keyed by string.
// T is typically a pointer type (e.g., *pgxpool.Pool, *cache.Client).
type Cache[T any] struct {
	mu      sync.Mutex
	items   map[string]*entry[T]
	sf      singleflight.Group
	maxSize int
	closer  func(T) // disposes of an evicted value
	label   string  // prefixed onto error messages, e.g. "mirrorstack/db: pool"
}

type entry[T any] struct {
	value    T
	lastUsed time.Time
	refCount int // protected by Cache.mu
}

// New creates a Cache. closer is called on every value that needs disposal
// (LRU eviction, factory's limit-error fallback, Close). label is prefixed
// onto error messages so callers can attribute pool-limit errors to a
// specific domain.
func New[T any](maxSize int, label string, closer func(T)) *Cache[T] {
	return &Cache[T]{
		items:   make(map[string]*entry[T]),
		maxSize: maxSize,
		closer:  closer,
		label:   label,
	}
}

// Get returns the value for key plus a release closure. If the key is not
// cached, factory is called to create it. The returned value is refcount-pinned
// until release runs — concurrent eviction cannot dispose of it. Pair every Get
// with a deferred release call.
//
// Concurrent Gets for the same missing key coalesce via singleflight: only one
// factory call runs, all callers receive the same value, and each caller's
// refcount is bumped exactly once.
func (c *Cache[T]) Get(key string, factory func() (T, error)) (T, func(), error) {
	var zero T

	// Fast path: cache hit. Bump refcount and return without holding the
	// lock across the caller's subsequent use of the value.
	c.mu.Lock()
	if e, ok := c.items[key]; ok {
		e.lastUsed = time.Now()
		e.refCount++
		c.mu.Unlock()
		return e.value, c.releaseFunc(key), nil
	}
	c.mu.Unlock()

	// Slow path: singleflight ensures only one factory call runs per key.
	// The leader's closure inserts the entry with refCount=1 (pinning it
	// against immediate eviction). Followers receive the broadcast value and
	// bump refcount by one each. Total refcount after N callers = N.
	var iAmLeader bool
	result, err, _ := c.sf.Do(key, func() (any, error) {
		iAmLeader = true
		value, err := factory()
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if len(c.items) >= c.maxSize {
			if !c.evictOldestLocked() {
				c.closer(value)
				return nil, fmt.Errorf("%s limit reached (%d), all entries have active references", c.label, c.maxSize)
			}
		}

		// refCount=1 pins the new entry against eviction during the brief
		// window before followers bump it. The leader's release will undo
		// this initial 1; followers' bumps + releases zero each other out.
		c.items[key] = &entry[T]{value: value, lastUsed: time.Now(), refCount: 1}
		return value, nil
	})
	if err != nil {
		return zero, nil, err
	}
	value := result.(T)

	if iAmLeader {
		// Factory closure already inserted with refCount=1 on our behalf.
		return value, c.releaseFunc(key), nil
	}

	// Follower: bump refcount on the leader's entry.
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.lastUsed = time.Now()
		e.refCount++
		return e.value, c.releaseFunc(key), nil
	}
	// Entry vanished between leader insert and follower bump. Only possible
	// if Close() ran concurrently — the leader's refCount=1 prevents normal
	// eviction. The value the leader returned is now closed; do not return it.
	return zero, nil, fmt.Errorf("%s entry evicted before follower could pin (cache likely closed)", c.label)
}

// releaseFunc returns a closure that decrements the refcount for key.
func (c *Cache[T]) releaseFunc(key string) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if e, ok := c.items[key]; ok && e.refCount > 0 {
			e.refCount--
		}
	}
}

// evictOldestLocked closes and removes the LRU non-pinned entry, skipping
// any entry with refCount > 0. Returns false if every entry is pinned. Must
// be called with c.mu held.
func (c *Cache[T]) evictOldestLocked() bool {
	var oldestKey string
	var oldestTime time.Time
	for key, e := range c.items {
		if e.refCount > 0 {
			continue
		}
		if oldestKey == "" || e.lastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = e.lastUsed
		}
	}
	if oldestKey == "" {
		return false
	}
	c.closer(c.items[oldestKey].value)
	delete(c.items, oldestKey)
	return true
}

// Close disposes of every cached value via the closer.
func (c *Cache[T]) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	old := c.items
	c.items = make(map[string]*entry[T])
	for _, e := range old {
		c.closer(e.value)
	}
}
