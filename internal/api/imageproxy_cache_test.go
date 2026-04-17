package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestImageProxy_ShardedLayout verifies that a cache miss writes the image into
// image-cache/<first2chars>/<key> (not flat at the top level).
func TestImageProxy_ShardedLayout(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", []byte("SHARDEDBODY"), http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	imgURL := upstream.URL + "/cover.jpg"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	sum := sha256.Sum256([]byte(imgURL))
	key := fmt.Sprintf("%x", sum)
	shard := key[:2]
	wantBody := filepath.Join(dir, "image-cache", shard, key)
	wantCT := wantBody + ".ct"

	if _, err := os.Stat(wantBody); err != nil {
		t.Errorf("body file not written at sharded path %s: %v", wantBody, err)
	}
	if _, err := os.Stat(wantCT); err != nil {
		t.Errorf("ct sidecar not written at sharded path %s: %v", wantCT, err)
	}

	flatBody := filepath.Join(dir, "image-cache", key)
	if _, err := os.Stat(flatBody); err == nil {
		t.Errorf("body unexpectedly present at flat path %s", flatBody)
	}
}

// TestImageProxy_ShardedCacheHit pre-seeds the sharded layout and verifies a
// cache hit from that structure without touching upstream.
func TestImageProxy_ShardedCacheHit(t *testing.T) {
	dir := t.TempDir()
	const rawURL = "https://example.com/preseed.jpg"
	sum := sha256.Sum256([]byte(rawURL))
	key := fmt.Sprintf("%x", sum)
	shard := key[:2]
	shardDir := filepath.Join(dir, "image-cache", shard)
	if err := os.MkdirAll(shardDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shardDir, key), []byte("SEEDED"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shardDir, key+".ct"), []byte("image/png"), 0o640); err != nil {
		t.Fatal(err)
	}

	h := NewImageProxyHandler(dir)
	h.validateURL = func(_ string) error { return nil }

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+rawURL, nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png (from sharded .ct)", ct)
	}
	if !strings.Contains(rr.Body.String(), "SEEDED") {
		t.Errorf("body = %q, want seeded content", rr.Body.String())
	}
}

// TestImageProxy_AtomicWrite_NoTmpLeft verifies that after a successful write,
// no .tmp files are left in the cache directory.
func TestImageProxy_AtomicWrite_NoTmpLeft(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", []byte("ATOMIC"), http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+upstream.URL+"/atomic.jpg", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	err := filepath.Walk(filepath.Join(dir, "image-cache"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			t.Errorf("leftover .tmp file after successful write: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
}

// TestImageProxy_StaleTmpIgnoredAsCache verifies that a stale .tmp file from a
// previous crashed write is not served as the cached image — the handler only
// serves <shard>/<key>, never <key>.tmp.
func TestImageProxy_StaleTmpIgnoredAsCache(t *testing.T) {
	dir := t.TempDir()
	upstream := newFakeUpstream("image/jpeg", []byte("FRESHBODY"), http.StatusOK)
	defer upstream.Close()

	h := newTestHandler(dir, upstream)
	imgURL := upstream.URL + "/stale.jpg"
	sum := sha256.Sum256([]byte(imgURL))
	key := fmt.Sprintf("%x", sum)
	shard := key[:2]
	shardDir := filepath.Join(dir, "image-cache", shard)
	if err := os.MkdirAll(shardDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shardDir, key+".tmp"), []byte("PARTIAL"), 0o640); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "FRESHBODY") {
		t.Errorf("body = %q, want fresh upstream content (not stale .tmp)", rr.Body.String())
	}
}

// TestImageProxy_ExpiredCacheRefetches covers the TTL branch: when an existing
// cached file is older than imageCacheTTL, a fresh upstream fetch must occur.
func TestImageProxy_ExpiredCacheRefetches(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", []byte("REFETCHED"), http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	imgURL := upstream.URL + "/expire.jpg"
	sum := sha256.Sum256([]byte(imgURL))
	key := fmt.Sprintf("%x", sum)
	shardDir := filepath.Join(dir, "image-cache", key[:2])
	if err := os.MkdirAll(shardDir, 0o750); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(shardDir, key)
	if err := os.WriteFile(oldPath, []byte("STALE"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-imageCacheTTL - time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "REFETCHED") {
		t.Errorf("body = %q, want REFETCHED (expired cache should refetch)", rr.Body.String())
	}
}

// TestHexKeyRe covers the key validation regex used by migrateFlatCache.
func TestHexKeyRe(t *testing.T) {
	valid := strings.Repeat("a", 64)
	cases := []struct {
		in   string
		want bool
	}{
		{valid, true},
		{strings.Repeat("0", 64), true},
		{strings.Repeat("f", 64), true},
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{strings.Repeat("A", 64), false},
		{strings.Repeat("a", 63), false},
		{strings.Repeat("a", 65), false},
		{strings.Repeat("g", 64), false},
		{"", false},
		{valid + ".ct", false},
		{valid + ".tmp", false},
		{"not-a-hash", false},
	}
	for _, tc := range cases {
		got := hexKeyRe.MatchString(tc.in)
		if got != tc.want {
			t.Errorf("hexKeyRe.MatchString(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestMigrateFlatCache_MovesFilesAndSidecars seeds a legacy flat cache layout
// and verifies that migrateFlatCache moves each hex-keyed file (and its .ct
// sidecar) into the sharded <first2chars>/<key> structure.
func TestMigrateFlatCache_MovesFilesAndSidecars(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatal(err)
	}

	key1 := strings.Repeat("a", 64)
	key2 := "bc" + strings.Repeat("1", 62)
	key3 := "de" + strings.Repeat("2", 62)

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(cacheDir, key1), "BODY1")
	mustWrite(filepath.Join(cacheDir, key1+".ct"), "image/jpeg")
	mustWrite(filepath.Join(cacheDir, key2), "BODY2")
	mustWrite(filepath.Join(cacheDir, key2+".ct"), "image/png")
	mustWrite(filepath.Join(cacheDir, key3), "BODY3")
	mustWrite(filepath.Join(cacheDir, "README"), "ignore me")

	h := &ImageProxyHandler{cacheDir: cacheDir}
	h.migrateFlatCache()

	check := func(key, wantBody, wantCT string) {
		t.Helper()
		shard := key[:2]
		bodyPath := filepath.Join(cacheDir, shard, key)
		b, err := os.ReadFile(bodyPath)
		if err != nil {
			t.Errorf("body for %s not migrated: %v", key, err)
			return
		}
		if string(b) != wantBody {
			t.Errorf("body for %s = %q, want %q", key, b, wantBody)
		}
		if wantCT != "" {
			ct, err := os.ReadFile(bodyPath + ".ct")
			if err != nil {
				t.Errorf("ct sidecar for %s not migrated: %v", key, err)
				return
			}
			if string(ct) != wantCT {
				t.Errorf("ct for %s = %q, want %q", key, ct, wantCT)
			}
		}
		if _, err := os.Stat(filepath.Join(cacheDir, key)); err == nil {
			t.Errorf("flat copy of %s still present after migration", key)
		}
	}
	check(key1, "BODY1", "image/jpeg")
	check(key2, "BODY2", "image/png")
	check(key3, "BODY3", "")

	if _, err := os.Stat(filepath.Join(cacheDir, "README")); err != nil {
		t.Errorf("non-hex file was unexpectedly moved/removed: %v", err)
	}
}

// TestMigrateFlatCache_MissingDir is a no-op when the cache dir doesn't exist.
func TestMigrateFlatCache_MissingDir(t *testing.T) {
	dir := t.TempDir()
	h := &ImageProxyHandler{cacheDir: filepath.Join(dir, "does-not-exist")}
	h.migrateFlatCache()
}

// TestMigrateFlatCache_SkipsDirsAndCTOnly ensures that directories (shards
// from a previous migration) and orphan .ct files are skipped rather than
// treated as legacy bodies.
func TestMigrateFlatCache_SkipsDirsAndCTOnly(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatal(err)
	}

	existingShard := filepath.Join(cacheDir, "ab")
	if err := os.MkdirAll(existingShard, 0o750); err != nil {
		t.Fatal(err)
	}
	existingKey := "ab" + strings.Repeat("3", 62)
	if err := os.WriteFile(filepath.Join(existingShard, existingKey), []byte("ALREADY"), 0o640); err != nil {
		t.Fatal(err)
	}

	orphanCT := filepath.Join(cacheDir, strings.Repeat("c", 64)+".ct")
	if err := os.WriteFile(orphanCT, []byte("image/jpeg"), 0o640); err != nil {
		t.Fatal(err)
	}

	h := &ImageProxyHandler{cacheDir: cacheDir}
	h.migrateFlatCache()

	if _, err := os.Stat(filepath.Join(existingShard, existingKey)); err != nil {
		t.Errorf("existing sharded file removed/moved: %v", err)
	}
	if _, err := os.Stat(orphanCT); err != nil {
		t.Errorf("orphan .ct file unexpectedly moved/removed: %v", err)
	}
}

// TestImageProxy_ConcurrentWrites fires many concurrent requests for distinct
// URLs at a cold cache. All should succeed and produce valid cache entries;
// no .tmp files should be left behind.
func TestImageProxy_ConcurrentWrites(t *testing.T) {
	upstream := newFakeUpstream("image/jpeg", []byte("CONCURRENT"), http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			u := fmt.Sprintf("%s/img-%d.jpg", upstream.URL, i)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+u, nil)
			rr := httptest.NewRecorder()
			h.Serve(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("url %d: status = %d, want 200", i, rr.Code)
			}
		}(i)
	}
	wg.Wait()

	bodies := 0
	tmps := 0
	_ = filepath.Walk(filepath.Join(dir, "image-cache"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".tmp"):
			tmps++
		case strings.HasSuffix(path, ".ct"):
			// sidecar
		default:
			bodies++
		}
		return nil
	})
	if tmps != 0 {
		t.Errorf("leftover .tmp files after concurrent writes: %d", tmps)
	}
	if bodies != n {
		t.Errorf("body file count = %d, want %d", bodies, n)
	}
}

// TestImageProxy_ConcurrentSameURL hammers the handler with concurrent requests
// for the same URL. The atomic rename pattern must guarantee every response
// sees a complete body — no truncated reads.
func TestImageProxy_ConcurrentSameURL(t *testing.T) {
	body := []byte("SAMEURLBODY")
	upstream := newFakeUpstream("image/jpeg", body, http.StatusOK)
	defer upstream.Close()

	dir := t.TempDir()
	h := newTestHandler(dir, upstream)
	imgURL := upstream.URL + "/same.jpg"

	const n = 30
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/images?url="+imgURL, nil)
			rr := httptest.NewRecorder()
			h.Serve(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rr.Code)
				return
			}
			got := rr.Body.Bytes()
			if len(got) != len(body) || string(got) != string(body) {
				t.Errorf("body = %q, want %q (partial/torn read?)", got, body)
			}
		}()
	}
	wg.Wait()
}
