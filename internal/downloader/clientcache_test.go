package downloader

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// qbitLoginCounter wires a tiny stub of the qBittorrent HTTP API and
// returns the underlying counter so a test can assert how many times
// Login() ran. The stub answers /auth/login with "Ok." (qBit v4.x success
// shape) and /torrents/info with an empty JSON array — enough for
// GetTorrents() to complete.
func qbitLoginCounter(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var logins atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &logins
}

// splitHostPort extracts host and integer port from an httptest.Server URL.
func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port atoi: %v", err)
	}
	return host, port
}

// withIsolatedCache swaps the package-global cache for a fresh one for the
// duration of the test, so concurrent test runs (or other tests in the
// package) do not see each other's entries.
func withIsolatedCache(t *testing.T) *ClientCache {
	t.Helper()
	fresh := NewClientCache()
	prev := SetDefaultCache(fresh)
	t.Cleanup(func() { SetDefaultCache(prev) })
	return fresh
}

func TestDownloader_PollReusesSession(t *testing.T) {
	// Five successive polls through the cache against the same downloader
	// config must trigger exactly one Login. Before the cache was
	// introduced (Wave 3 finding 10), each poll re-built the qBittorrent
	// client, throwing away the SID cookie and forcing a fresh Login.
	srv, logins := qbitLoginCounter(t)
	cache := withIsolatedCache(t)
	host, port := splitHostPort(t, srv.URL)

	client := &models.DownloadClient{
		ID:       42,
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "admin",
		Password: "secret",
	}

	for i := 0; i < 5; i++ {
		qb := cache.QbittorrentFor(client)
		// Drive the same path the scanner uses: GetTorrents triggers
		// ensureLoggedIn → Login on the first call only.
		if _, err := qb.GetTorrents(context.Background(), ""); err != nil {
			t.Fatalf("poll %d: GetTorrents: %v", i, err)
		}
	}

	if got := logins.Load(); got != 1 {
		t.Fatalf("expected 1 login across 5 polls, got %d", got)
	}
	if got := cache.ConstructorCount(); got != 1 {
		t.Fatalf("expected 1 client construction, got %d", got)
	}
}

func TestDownloader_ConfigUpdateEvictsCache(t *testing.T) {
	// Updating the downloader config via the cache must drop the cached
	// entry so the next poll builds a fresh client (and therefore Logs in
	// again with the new credentials). This is the eviction hook called
	// from the API handler's Update / Delete paths.
	srv, logins := qbitLoginCounter(t)
	cache := withIsolatedCache(t)
	host, port := splitHostPort(t, srv.URL)

	client := &models.DownloadClient{
		ID:       7,
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "admin",
		Password: "old-secret",
	}

	// First poll: cache miss, one Login.
	qb1 := cache.QbittorrentFor(client)
	if _, err := qb1.GetTorrents(context.Background(), ""); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if got := logins.Load(); got != 1 {
		t.Fatalf("after first poll: want 1 login, got %d", got)
	}

	// Admin rotates the password. The handler calls cache.Evict.
	client.Password = "new-secret"
	cache.Evict(client.ID)

	// Next poll: must build a fresh client and Login again with the new
	// credentials.
	qb2 := cache.QbittorrentFor(client)
	if qb1 == qb2 {
		t.Fatal("expected a fresh *qbittorrent.Client after eviction; cache returned the stale instance")
	}
	if _, err := qb2.GetTorrents(context.Background(), ""); err != nil {
		t.Fatalf("post-eviction poll: %v", err)
	}
	if got := logins.Load(); got != 2 {
		t.Fatalf("after eviction + repoll: want 2 logins, got %d", got)
	}
	if got := cache.ConstructorCount(); got != 2 {
		t.Fatalf("expected 2 constructions (initial + post-eviction), got %d", got)
	}
}

func TestDownloader_FingerprintDriftEvictsAutomatically(t *testing.T) {
	// Defensive: if an admin mutates the credential AND somehow the
	// explicit Evict hook fails to fire (race, missing call site,
	// whatever), the cache itself notices a drift in the config
	// fingerprint and rebuilds. Confirms the safety net works.
	srv, logins := qbitLoginCounter(t)
	cache := withIsolatedCache(t)
	host, port := splitHostPort(t, srv.URL)

	client := &models.DownloadClient{
		ID:       9,
		Type:     "qbittorrent",
		Host:     host,
		Port:     port,
		Username: "admin",
		Password: "v1",
	}
	qb1 := cache.QbittorrentFor(client)
	if _, err := qb1.GetTorrents(context.Background(), ""); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	// Same ID, new password, no Evict call.
	client.Password = "v2"
	qb2 := cache.QbittorrentFor(client)
	if qb1 == qb2 {
		t.Fatal("fingerprint drift must yield a fresh client")
	}
	if _, err := qb2.GetTorrents(context.Background(), ""); err != nil {
		t.Fatalf("drift poll: %v", err)
	}
	if got := logins.Load(); got != 2 {
		t.Fatalf("want 2 logins after fingerprint drift, got %d", got)
	}
}

func TestDownloader_DistinctIDsGetDistinctClients(t *testing.T) {
	// Two DownloadClient rows (different IDs) must not collide in the
	// cache, even if they happen to share a host (e.g. two qBit instances
	// behind the same reverse proxy on different URLBases).
	srv, _ := qbitLoginCounter(t)
	cache := withIsolatedCache(t)
	host, port := splitHostPort(t, srv.URL)

	a := &models.DownloadClient{ID: 1, Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}
	b := &models.DownloadClient{ID: 2, Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p"}

	qa := cache.QbittorrentFor(a)
	qb := cache.QbittorrentFor(b)
	if qa == qb {
		t.Fatal("distinct DownloadClient IDs must yield distinct cache entries")
	}
	if cache.Len() != 2 {
		t.Fatalf("expected 2 cache entries, got %d", cache.Len())
	}
}

func TestDownloader_EvictAllClears(t *testing.T) {
	srv, _ := qbitLoginCounter(t)
	cache := withIsolatedCache(t)
	host, port := splitHostPort(t, srv.URL)

	for i := int64(1); i <= 3; i++ {
		_ = cache.QbittorrentFor(&models.DownloadClient{
			ID: i, Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p",
		})
	}
	if cache.Len() != 3 {
		t.Fatalf("setup: expected 3 entries, got %d", cache.Len())
	}
	cache.EvictAll()
	if cache.Len() != 0 {
		t.Fatalf("EvictAll: expected 0 entries, got %d", cache.Len())
	}
}
