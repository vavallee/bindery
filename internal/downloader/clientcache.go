// Package downloader's clientCache pools long-lived downloader clients
// (qBittorrent, Transmission, Deluge, NZBGet, SABnzbd) so the scanner's
// 15-second poll cycle stops re-Login-ing every time. This was finding 10
// of the Wave 3 deep audit: every poll cycle the scanner ran something
// like qbittorrent.New(...), throwing away the SID cookie qBit had just
// issued, throwing away Transmission's X-Transmission-Session-Id (which
// then forces a 409-retry on the next request), throwing away Deluge's
// session cookie, and throwing away the kernel-level TCP keep-alives on
// every fresh *http.Client.
//
// Caching strategy: one cache entry per models.DownloadClient.ID. The
// entry is invalidated when the entry's configFingerprint (host, port,
// user, urlbase, ssl flag, type, password/apikey) drifts — so an admin's
// PUT /api/v1/downloadclient/:id is honoured immediately on the next
// poll. Public Evict(id) gives the API handler an explicit hook (also
// called on Delete).
//
// Auth-failure recovery story: each downloader client already handles a
// stale session transparently. qBittorrent.get() checks for HTTP 403 and
// re-runs Login() then retries the request once. Deluge.call() checks for
// HTTP 401 and re-Logins. Transmission.doRequest() picks up a fresh
// X-Transmission-Session-Id from a 409 and retries. Caching does not
// disrupt that recovery: when the cached client's session expires on the
// remote side, the next request hits the existing retry-with-relogin path.
package downloader

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"sync/atomic"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/downloader/transmission"
	"github.com/vavallee/bindery/internal/models"
)

// cachedEntry holds whichever typed client matches the DownloadClient.Type
// along with the config fingerprint at construction. Exactly one of the
// pointers is non-nil per entry.
type cachedEntry struct {
	fingerprint  string
	qbittorrent  *qbittorrent.Client
	transmission *transmission.Client
	deluge       *deluge.Client
	nzbget       *nzbget.Client
	sabnzbd      *sabnzbd.Client
}

// ClientCache memoises one downloader client per DownloadClient.ID. Safe
// for concurrent use by the scanner goroutine and API request goroutines.
type ClientCache struct {
	mu               sync.Mutex
	entries          map[int64]*cachedEntry
	constructorCount atomic.Int64
}

// NewClientCache returns an empty cache.
func NewClientCache() *ClientCache {
	return &ClientCache{entries: make(map[int64]*cachedEntry)}
}

// defaultCache is the process-wide cache used by adapter.go and scanner
// helpers. The API handler reaches it via Evict() to invalidate on
// config updates.
var defaultCache = NewClientCache()

// DefaultCache returns the package-global cache. Exposed so callers
// constructing a Scanner / API handler can share a single instance, and
// so tests can swap it out via SetDefaultCache.
func DefaultCache() *ClientCache {
	return defaultCache
}

// SetDefaultCache replaces the package-global cache and returns the
// previous instance. Intended for tests that want isolation; production
// code should not call this.
func SetDefaultCache(c *ClientCache) *ClientCache {
	prev := defaultCache
	defaultCache = c
	return prev
}

// Evict drops the cached client for the given DownloadClient.ID. Called
// by the API handler's Update and Delete paths so the next poll observes
// the new credentials immediately rather than waiting for an auth-failure
// retry.
func Evict(id int64) {
	defaultCache.Evict(id)
}

// Evict drops a single entry from this cache.
func (c *ClientCache) Evict(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}

// EvictAll clears every cached entry. Useful in tests; not currently
// called from production code.
func (c *ClientCache) EvictAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[int64]*cachedEntry)
}

// ConstructorCount returns how many downloader clients have been
// constructed via this cache (i.e. cache-miss count). Tests assert this
// stays at 1 across N polls against the same config; production callers
// should not rely on this value.
func (c *ClientCache) ConstructorCount() int64 {
	return c.constructorCount.Load()
}

// Len returns the number of cached entries.
func (c *ClientCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// configFingerprint hashes the connection-identifying fields of a
// DownloadClient. Including password/apiKey here is deliberate: when an
// admin rotates a credential we want the cache to evict-and-rebuild on
// the next poll. The hash is a SHA-256 prefix so a leaked log line
// (unlikely, but defensive) doesn't expose the cleartext credential.
func configFingerprint(c *models.DownloadClient) string {
	h := sha256.New()
	// Use a NUL separator so adjacent field collisions are impossible
	// (e.g. host="foo", port="1234" must not hash the same as host="foo1",
	// port="234").
	for _, s := range []string{
		c.Type,
		c.Host,
		c.URLBase,
		c.Username,
		c.Password,
		c.APIKey,
	} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	// Numeric / bool fields appended last; their byte representation is
	// stable across runs.
	var buf [9]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(c.Port))
	if c.UseSSL {
		buf[8] = 1
	}
	h.Write(buf[:])
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// lookupOrBuild returns the cached entry whose fingerprint matches the
// supplied client config, constructing a fresh one if either no entry
// exists yet or the fingerprint has drifted.
func (c *ClientCache) lookupOrBuild(client *models.DownloadClient, build func() *cachedEntry) *cachedEntry {
	fp := configFingerprint(client)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[client.ID]; ok && e.fingerprint == fp {
		return e
	}
	// Either first poll or config has drifted since the previous poll.
	// In the drift case we silently swap; the old entry's session/cookies
	// become garbage and will age out via the http.Transport's
	// IdleConnTimeout. The replacement attempt happens here under the
	// same mutex so two concurrent pollers don't both build.
	c.constructorCount.Add(1)
	e := build()
	e.fingerprint = fp
	c.entries[client.ID] = e
	return e
}

// QbittorrentFor returns the cached qBittorrent client for the supplied
// DownloadClient config, building a fresh one on a cache miss. The
// returned client's loggedIn flag and cookie jar are preserved across
// successive calls so polling no longer triggers re-Login per cycle.
func QbittorrentFor(c *models.DownloadClient) *qbittorrent.Client {
	return defaultCache.QbittorrentFor(c)
}

// QbittorrentFor is the method form for callers that hold their own cache.
func (c *ClientCache) QbittorrentFor(client *models.DownloadClient) *qbittorrent.Client {
	e := c.lookupOrBuild(client, func() *cachedEntry {
		return &cachedEntry{qbittorrent: qbittorrent.New(
			client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL,
		)}
	})
	return e.qbittorrent
}

// TransmissionFor returns the cached Transmission client.
func TransmissionFor(c *models.DownloadClient) *transmission.Client {
	return defaultCache.TransmissionFor(c)
}

// TransmissionFor is the method form.
func (c *ClientCache) TransmissionFor(client *models.DownloadClient) *transmission.Client {
	e := c.lookupOrBuild(client, func() *cachedEntry {
		return &cachedEntry{transmission: transmission.New(
			client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL,
		)}
	})
	return e.transmission
}

// DelugeFor returns the cached Deluge client.
func DelugeFor(c *models.DownloadClient) *deluge.Client {
	return defaultCache.DelugeFor(c)
}

// DelugeFor is the method form.
func (c *ClientCache) DelugeFor(client *models.DownloadClient) *deluge.Client {
	e := c.lookupOrBuild(client, func() *cachedEntry {
		return &cachedEntry{deluge: deluge.New(
			client.Host, client.Port, client.Password, client.URLBase, client.UseSSL,
		)}
	})
	return e.deluge
}

// NzbgetFor returns the cached NZBGet client.
func NzbgetFor(c *models.DownloadClient) *nzbget.Client {
	return defaultCache.NzbgetFor(c)
}

// NzbgetFor is the method form.
func (c *ClientCache) NzbgetFor(client *models.DownloadClient) *nzbget.Client {
	e := c.lookupOrBuild(client, func() *cachedEntry {
		return &cachedEntry{nzbget: nzbget.New(
			client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL,
		)}
	})
	return e.nzbget
}

// SabnzbdFor returns the cached SABnzbd client.
func SabnzbdFor(c *models.DownloadClient) *sabnzbd.Client {
	return defaultCache.SabnzbdFor(c)
}

// SabnzbdFor is the method form.
func (c *ClientCache) SabnzbdFor(client *models.DownloadClient) *sabnzbd.Client {
	e := c.lookupOrBuild(client, func() *cachedEntry {
		return &cachedEntry{sabnzbd: sabnzbd.New(
			client.Host, client.Port, client.APIKey, client.URLBase, client.UseSSL,
		)}
	})
	return e.sabnzbd
}
