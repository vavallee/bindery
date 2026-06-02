package metadata

import (
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestTTLCache_SetAndGet(t *testing.T) {
	c := newTTLCache(time.Minute)
	c.set("key1", "value1")
	v, ok := c.get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v.(string) != "value1" {
		t.Errorf("want 'value1', got %q", v)
	}
}

func TestTTLCache_Miss(t *testing.T) {
	c := newTTLCache(time.Minute)
	_, ok := c.get("missing")
	if ok {
		t.Error("expected cache miss for unknown key")
	}
}

func TestTTLCache_Expiry(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("k", "v")
	time.Sleep(2 * time.Millisecond)
	_, ok := c.get("k")
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestTTLCache_Cleanup(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("a", 1)
	c.set("b", 2)
	time.Sleep(2 * time.Millisecond)
	c.cleanup()

	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 items after cleanup, got %d", n)
	}
}

// TestTTLCache_EvictsByCount fills the cache past its cap and asserts the
// entry count never exceeds the cap. Backstops finding 11: between the
// hourly janitor sweeps a hot fan-out could grow the items map without
// bound.
func TestTTLCache_EvictsByCount(t *testing.T) {
	const cap = 4
	c := newTTLCacheWithCap(time.Hour, cap)
	for i := 0; i < cap*3; i++ {
		c.set(strconv.Itoa(i), i)
	}
	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n > cap {
		t.Errorf("len(items) = %d, want <= %d", n, cap)
	}
}

// TestTTLCache_EvictionPolicyPicksEarliestExpiry stages entries with
// staggered TTLs, fills to cap with a brand-new entry, and asserts the
// overflow eviction targets the entry that would have expired soonest,
// not the most-recently-added.
func TestTTLCache_EvictionPolicyPicksEarliestExpiry(t *testing.T) {
	c := newTTLCacheWithCap(time.Hour, 3)
	// Manually plant entries with controlled expiresAt values so the
	// test doesn't race the wall clock.
	now := time.Now()
	c.state.mu.Lock()
	c.state.items["soonest"] = cacheItem{value: "s", expiresAt: now.Add(10 * time.Millisecond)}
	c.state.items["middle"] = cacheItem{value: "m", expiresAt: now.Add(1 * time.Hour)}
	c.state.items["latest"] = cacheItem{value: "l", expiresAt: now.Add(2 * time.Hour)}
	c.state.mu.Unlock()

	// This set overflows the cap. The earliest-expiry entry should go.
	c.set("fresh", "f")

	c.state.mu.RLock()
	_, soonestPresent := c.state.items["soonest"]
	_, middlePresent := c.state.items["middle"]
	_, latestPresent := c.state.items["latest"]
	_, freshPresent := c.state.items["fresh"]
	c.state.mu.RUnlock()

	if soonestPresent {
		t.Error("expected earliest-expiring entry 'soonest' to be evicted")
	}
	if !middlePresent {
		t.Error("expected 'middle' to survive eviction")
	}
	if !latestPresent {
		t.Error("expected 'latest' to survive eviction")
	}
	if !freshPresent {
		t.Error("expected just-inserted 'fresh' to survive eviction")
	}
}

// TestTTLCache_TTLStillEvicts pins the original TTL semantics: even with
// the cap policy added, a past-TTL get is still a miss. Catches the
// regression of skipping the time.After check after a successful map
// lookup.
func TestTTLCache_TTLStillEvicts(t *testing.T) {
	c := newTTLCacheWithCap(time.Nanosecond, 100)
	c.set("k", "v")
	time.Sleep(2 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Error("expected cache miss after TTL expiry under capped cache")
	}
}

// TestTTLCache_EvictionRaceSafe pounds the cache from multiple goroutines
// at saturation so go test -race can flag any read-write overlap on the
// eviction path. evictEarliestLocked iterates the map while holding the
// write lock, but a regression that downgraded the lock or reordered the
// branch would surface here.
func TestTTLCache_EvictionRaceSafe(t *testing.T) {
	c := newTTLCacheWithCap(time.Minute, 8)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := "w" + strconv.Itoa(worker) + "-" + strconv.Itoa(i)
				c.set(key, i)
				_, _ = c.get(key)
			}
		}(w)
	}
	wg.Wait()
	c.state.mu.RLock()
	n := len(c.state.items)
	c.state.mu.RUnlock()
	if n > 8 {
		t.Errorf("len(items) = %d after concurrent writes, want <= 8", n)
	}
}

// TestTTLCache_JanitorExitsOnGC verifies the runtime.AddCleanup hook
// closes the done channel so the janitor goroutine doesn't leak when the
// cache value is collected. Without this, every test that constructs an
// Aggregator (which embeds a ttlCache) used to leak one goroutine each,
// which compounded into the internal/api 10-min CI timeout (#73).
func TestTTLCache_JanitorExitsOnGC(t *testing.T) {
	// Capture the done channel before we lose the cache reference.
	c := newTTLCache(time.Hour)
	done := c.done

	// Drop the only reference and force GC + cleanup execution.
	c = nil
	runtime.GC()
	runtime.GC() // a second pass ensures AddCleanup callbacks have fired

	select {
	case <-done:
		// expected: done closed by the cleanup callback
	case <-time.After(2 * time.Second):
		t.Fatal("done channel was not closed within 2s of GC; janitor would leak")
	}
}
