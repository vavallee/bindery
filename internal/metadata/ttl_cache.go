package metadata

import (
	"runtime"
	"sync"
	"time"
)

// ttlCache is a goroutine-safe key/value cache with per-entry expiry. A
// background janitor sweeps expired items hourly so the map doesn't grow
// without bound when keys are written but never re-read.
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
	mu    sync.RWMutex
	items map[string]cacheItem
}

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	state := &ttlState{items: make(map[string]cacheItem)}
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
