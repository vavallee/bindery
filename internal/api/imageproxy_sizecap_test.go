package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// countingUpstream serves body as a jpeg and counts the requests it gets.
func countingUpstream(body []byte) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	}))
	return srv, &hits
}

func serveImage(t *testing.T, h *ImageProxyHandler, rawURL string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+rawURL, nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	return rr
}

// TestImageProxy_FullCacheServesWithoutWriting: once the cache holds as much
// as its cap allows, an image that is not cached yet is still fetched and
// served, but nothing is written for it. Age was the only limit before, so
// the cache could grow until the volume it shares with the database was full.
func TestImageProxy_FullCacheServesWithoutWriting(t *testing.T) {
	body := []byte("\xff\xd8\xffFAKEJPEGBODYBYTES")
	upstream, hits := countingUpstream(body)
	defer upstream.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	if err := os.MkdirAll(filepath.Join(cacheDir, "ab"), 0o750); err != nil {
		t.Fatal(err)
	}
	// An entry already on disk that puts the cache over a 64 byte cap.
	if err := os.WriteFile(filepath.Join(cacheDir, "ab", "existing"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newTestHandler(dir, upstream).WithMaxCacheBytes(64)
	// This test is about the refusal alone. Mark the early sweep as just
	// asked for so it does not run and make room behind the assertions.
	h.fullSweepAt = time.Now()

	for i := 1; i <= 2; i++ {
		rr := serveImage(t, h, upstream.URL+"/new.jpg")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rr.Code)
		}
		if rr.Body.String() != string(body) {
			t.Errorf("request %d: body = %q, want the upstream image", i, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("request %d: Content-Type = %q, want image/jpeg", i, ct)
		}
	}

	if got := dirBytes(t, cacheDir); got != 4096 {
		t.Errorf("cache holds %d bytes after serving past the cap, want the original 4096 (nothing written)", got)
	}
	// Not cached, so the second request went upstream again.
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits = %d, want 2", got)
	}
	if size, _ := h.CacheSize(); size != 4096 {
		t.Errorf("CacheSize = %d, want 4096", size)
	}
}

// TestImageProxy_WritesContinueUnderCap: below the cap nothing changes. Each
// new image is written and served from disk afterwards, right up to the
// write that would cross the cap, which is served but not stored.
func TestImageProxy_WritesContinueUnderCap(t *testing.T) {
	body := []byte("\xff\xd8\xffFAKEJPEGBODYBYTES")
	upstream, hits := countingUpstream(body)
	defer upstream.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	h := newTestHandler(dir, upstream)
	if h.maxBytes != DefaultImageCacheMaxBytes || h.MaxCacheBytes() != DefaultImageCacheMaxBytes {
		t.Fatalf("default cap = %d, want %d", h.maxBytes, DefaultImageCacheMaxBytes)
	}

	if rr := serveImage(t, h, upstream.URL+"/one.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("one.jpg: status = %d, want 200", rr.Code)
	}
	entry := dirBytes(t, cacheDir)
	if entry == 0 {
		t.Fatal("first image was not written to the cache")
	}

	// Room for exactly one more entry.
	h.WithMaxCacheBytes(2 * entry)
	h.fullSweepAt = time.Now() // no early sweep, see the test above
	if rr := serveImage(t, h, upstream.URL+"/two.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("two.jpg: status = %d, want 200", rr.Code)
	}
	if got := dirBytes(t, cacheDir); got != 2*entry {
		t.Fatalf("cache holds %d bytes after the second image, want %d", got, 2*entry)
	}

	// Cached entries are still served from disk with the cache full.
	before := hits.Load()
	if rr := serveImage(t, h, upstream.URL+"/two.jpg"); rr.Code != http.StatusOK || rr.Body.String() != string(body) {
		t.Fatalf("cached two.jpg: status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if hits.Load() != before {
		t.Error("a cached image went upstream again")
	}

	// The cache is exactly full now, so a third image is served, not stored.
	if rr := serveImage(t, h, upstream.URL+"/three.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("three.jpg: status = %d, want 200", rr.Code)
	}
	if got := dirBytes(t, cacheDir); got != 2*entry {
		t.Errorf("cache holds %d bytes after a write past the cap, want %d", got, 2*entry)
	}
}
