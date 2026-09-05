package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dirBytes sums every regular file under root, which is what CacheSize's walk
// is supposed to produce.
func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a missing cache dir means zero bytes
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // raced with a delete; not this test's concern
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return total
}

// TestImageProxy_CacheSizeIsMemoised is the regression test for #2340: the old
// CacheSize walked the whole cache tree on every call, and /system/status calls
// it on every request. After the fix the walk runs at most once per
// imageCacheSizeTTL, which shows up as a second call NOT noticing a file
// written behind the handler's back.
func TestImageProxy_CacheSizeIsMemoised(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	if err := os.MkdirAll(filepath.Join(cacheDir, "ab"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "ab", "first"), make([]byte, 100), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := NewImageProxyHandler(dir)

	first, err := h.CacheSize()
	if err != nil {
		t.Fatalf("CacheSize: %v", err)
	}
	if first != 100 {
		t.Fatalf("first CacheSize = %d, want 100", first)
	}

	// Written outside the handler, so nothing adjusts the memo.
	if err := os.WriteFile(filepath.Join(cacheDir, "ab", "second"), make([]byte, 250), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	second, err := h.CacheSize()
	if err != nil {
		t.Fatalf("CacheSize: %v", err)
	}
	if second != 100 {
		t.Errorf("second CacheSize = %d, want the memoised 100 (a re-walk means the memo is not working)", second)
	}

	// Expiring the memo brings the number back in line.
	h.sizeMu.Lock()
	h.sizeAt = time.Now().Add(-imageCacheSizeTTL - time.Minute)
	h.sizeMu.Unlock()

	third, err := h.CacheSize()
	if err != nil {
		t.Fatalf("CacheSize: %v", err)
	}
	if third != 350 {
		t.Errorf("CacheSize after the TTL expired = %d, want 350", third)
	}
}

// TestImageProxy_CacheSizeTracksWrites checks the incremental half of #2340:
// between full recounts, a cache write adjusts the memo by its real on-disk
// delta, so the memoised number stays equal to what a walk would report.
func TestImageProxy_CacheSizeTracksWrites(t *testing.T) {
	body := []byte("FAKEJPEGBODYBYTES")
	upstream := newFakeUpstream("image/jpeg", body, http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	// Seed the memo while the cache directory does not exist yet. The walk
	// errors, which is fine; the total is zero either way.
	if size, _ := h.CacheSize(); size != 0 {
		t.Fatalf("CacheSize on an empty cache = %d, want 0", size)
	}

	for _, name := range []string{"/one.jpg", "/two.jpg"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+name, nil)
		rr := httptest.NewRecorder()
		h.Serve(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("Serve(%s) = %d, want 200", name, rr.Code)
		}
	}

	want := dirBytes(t, filepath.Join(dir, "image-cache"))
	if want == 0 {
		t.Fatal("nothing was written to the cache")
	}
	got, err := h.CacheSize()
	if err != nil {
		t.Fatalf("CacheSize: %v", err)
	}
	if got != want {
		t.Errorf("CacheSize = %d, want %d (the on-disk total)", got, want)
	}

	// A refetch that overwrites an existing entry must not double count: the
	// delta is measured on both sides of the write, not taken from len(body).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/one.jpg", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if got, err := h.CacheSize(); err != nil || got != want {
		t.Errorf("CacheSize after a cache hit = %d (err %v), want %d", got, err, want)
	}
}

// TestImageProxy_EvictionRecountsCacheSize covers the sweep half of #2340: the
// daily eviction goroutine is the memo's periodic source of truth, so dropping
// an expired entry must be reflected without waiting for imageCacheSizeTTL.
func TestImageProxy_EvictionRecountsCacheSize(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache", "cd")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(cacheDir, "stale")
	if err := os.WriteFile(stale, make([]byte, 500), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := time.Now().Add(-imageCacheTTL - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	h := NewImageProxyHandler(dir)
	if size, err := h.CacheSize(); err != nil || size != 500 {
		t.Fatalf("CacheSize before eviction = %d (err %v), want 500", size, err)
	}

	h.evictExpired()

	if size, err := h.CacheSize(); err != nil || size != 0 {
		t.Errorf("CacheSize after eviction = %d (err %v), want 0", size, err)
	}
}
