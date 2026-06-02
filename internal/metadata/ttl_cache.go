package metadata

import (
	"runtime"
	"sync"
	"time"
)

// ttlCache is a goroutine-safe key/value cache with per-entry expiry and a
// hard cap on entry count. A background janitor sweeps expired items hourly
// so the map doesn't grow without bound when keys are written but never
// re-read; the count cap is the backstop for the in-between window where a
// hot ingestion path can fill the cache faster than the hourly sweep evicts.
//
// Eviction policy: when set() would push the entry count past maxEntries,
// the cache evicts the entry with the earliest expiresAt. That's the entry
// that would have been dropped soonest anyway, so it's the cheapest correct
// policy. It's an O(n) scan on every overflow set, which is fine at the
// default cap of 10000 entries and the access frequency this cache sees
// (metadata fan-out, not a request-path cache). Switch to container/list
// LRU if the cap ever needs to climb past ~100k. We deliberately avoid
// pulling in a third-party LRU package; this cache is internal and the
// no-deps policy keeps the package vendorable.
//
// The janitor's lifetime is tied to the cache value via runtime.AddCleanup:
// when the cache becomes unreachable, the cleanup fires and the janitor
// goroutine exits. This is the production-safe equivalent of "the cache
// owner forgot to call Close." Tests that create-and-throw-away aggregators
// (which embed a ttlCache) used to leak one janitor goroutine each, and
// after a few hundred fixtures the leak choked CI's test scheduler and
// caused validate(Go) to flake out at the 10-minute timeout.
//
// The janitor closure must NOT reference the *ttlCache value, only its
// internal state (the items map + mutex via the ttlState pointer) plus
// the shutdown channel. Holding a reference to the cache itself would
// pin it to the heap and defeat the GC-driven cleanup.
type ttlCache struct {
	state *ttlState
	done  chan struct{}
	ttl   time.Duration
}

type ttlState struct {
	mu         sync.RWMutex
	items      map[string]cacheItem
	maxEntries int
}

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
}

// defaultMaxEntries caps every aggregator-scoped ttlCache. 10k entries at
// the heaviest realistic value size (~50KB per cached []models.Book on a
// prolific-author fan-out) bounds the cache at roughly 500MB worst-case,
// which is the rough ceiling we want for a single aggregator instance.
const defaultMaxEntries = 10000

func newTTLCache(ttl time.Duration) *ttlCache {
	return newTTLCacheWithCap(ttl, defaultMaxEntries)
}

// newTTLCacheWithCap is the explicit-cap constructor used by tests that need
// to exercise the eviction policy without filling 10k entries. Production
// callers go through newTTLCache.
func newTTLCacheWithCap(ttl time.Duration, maxEntries int) *ttlCache {
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	state := &ttlState{
		items:      make(map[string]cacheItem),
		maxEntries: maxEntries,
	}
	done := make(chan struct{})

	go runJanitor(state, done)

	c := &ttlCache{state: state, done: done, ttl: ttl}
	// Fires when c becomes unreachable. Closing done is idempotent because
	// AddCleanup runs at most once per registered argument; the janitor's
	// select on done will then return.
	runtime.AddCleanup(c, func(d chan struct{}) {
		close(d)
	}, done)
	return c
}

// runJanitor sweeps expired entries from state every hour until done is
// closed. Lives in package scope and takes its dependencies as parameters
// (not as a closure over *ttlCache) so it doesn't pin the cache to the heap.
func runJanitor(state *ttlState, done chan struct{}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			state.cleanup()
		case <-done:
			return
		}
	}
}

func (c *ttlCache) get(key string) (interface{}, bool) {
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()

	item, ok := c.state.items[key]
	if !ok || time.Now().After(item.expiresAt) {
		return nil, false
	}
	return item.value, true
}

func (c *ttlCache) set(key string, value interface{}) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	c.state.items[key] = cacheItem{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	// Evict only when the cap is exceeded. Overwriting an existing key
	// doesn't grow the map, so this branch is a no-op in the steady state.
	if c.state.maxEntries > 0 && len(c.state.items) > c.state.maxEntries {
		c.state.evictEarliestLocked(key)
	}
}

// evictEarliestLocked removes the entry with the smallest expiresAt and is
// invoked with state.mu held in write mode. It refuses to evict the key
// just inserted by the caller so a single set() can never delete its own
// write (matters when every existing entry happens to share an expiresAt
// that compares strictly less than the just-written one only by chance).
func (s *ttlState) evictEarliestLocked(justInserted string) {
	var (
		victim       string
		victimExpiry time.Time
		found        bool
	)
	for k, v := range s.items {
		if k == justInserted {
			continue
		}
		if !found || v.expiresAt.Before(victimExpiry) {
			victim = k
			victimExpiry = v.expiresAt
			found = true
		}
	}
	if found {
		delete(s.items, victim)
	}
}

// cleanup delegates to the underlying state. Exposed on the cache type so
// tests don't have to reach through to the state.
func (c *ttlCache) cleanup() { c.state.cleanup() }

func (s *ttlState) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for k, v := range s.items {
		if now.After(v.expiresAt) {
			delete(s.items, k)
		}
	}
}
