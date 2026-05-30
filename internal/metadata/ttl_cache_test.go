package metadata

import (
	"runtime"
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
