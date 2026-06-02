// Package indexer's clientCache pools *newznab.Client instances keyed on the
// indexer's URL + apiKey. Without this every SearchBook call instantiated a
// fresh client (finding 9 of the Wave 3 deep audit), which paired with the
// per-call http.Transport meant a full TCP+TLS handshake on every search.
//
// The shared transport is owned by the newznab package itself
// (sharedTransportInstance) so connection reuse already happens at the
// transport layer. This cache is the second optimisation: skip the per-call
// *Client allocation and URL-normalisation work, and give tests a stable
// hook (constructorCount) for asserting that repeated searches against the
// same configuration construct exactly one client.
package indexer

import (
	"sync"
	"sync/atomic"

	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// clientCache holds one *newznab.Client per (baseURL, apiKey) tuple. The
// apiKey is part of the key because it determines the credentials used on
// every outbound request; the underlying http.Transport is shared
// process-wide so credentials are deliberately not the cache-eviction
// trigger — when an admin rotates an apikey, a fresh entry is created and
// the old client's idle connections age out via IdleConnTimeout.
type clientCache struct {
	mu      sync.Mutex
	clients map[string]*newznab.Client
	// constructorCount counts cache misses; tests assert this stays at 1
	// for N searches against the same indexer config.
	constructorCount atomic.Int64
	// newFn is the factory used on a cache miss. nil means newznab.New;
	// tests can inject a hook that bypasses the SSRF dialer.
	newFn func(baseURL, apiKey string) *newznab.Client
}

// newClientCache constructs an empty cache. newFn may be nil; callers can
// override it after construction (tests do this to inject httptest factories).
func newClientCache(newFn func(baseURL, apiKey string) *newznab.Client) *clientCache {
	return &clientCache{
		clients: make(map[string]*newznab.Client),
		newFn:   newFn,
	}
}

// get returns the cached client for the given configuration, constructing
// it on the first miss. Safe for concurrent use.
func (c *clientCache) get(baseURL, apiKey string) *newznab.Client {
	key := baseURL + "\x00" + apiKey
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.clients[key]; ok {
		return cl
	}
	c.constructorCount.Add(1)
	var cl *newznab.Client
	if c.newFn != nil {
		cl = c.newFn(baseURL, apiKey)
	} else {
		cl = newznab.New(baseURL, apiKey)
	}
	c.clients[key] = cl
	return cl
}

// ConstructorCount returns the number of cache misses (i.e. clients
// actually built). Exposed for tests asserting the pooling invariant.
func (c *clientCache) ConstructorCount() int64 {
	return c.constructorCount.Load()
}

// Len returns the number of cached clients. Helper for tests asserting
// that distinct configs produce distinct entries.
func (c *clientCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.clients)
}
