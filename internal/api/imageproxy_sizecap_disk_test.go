package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// cacheFiles counts the regular files under the cache directory.
func cacheFiles(t *testing.T, root string) int64 {
	t.Helper()
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// TestImageProxy_SmallBodiesCannotExceedCapOnDisk: the cap is on what the
// cache takes on disk, not on the sum of file lengths. Every cached file
// holds at least one 4 KiB block, so many very small bodies used to add up
// to almost nothing against the cap while taking far more than it on disk.
func TestImageProxy_SmallBodiesCannotExceedCapOnDisk(t *testing.T) {
	upstream, _ := countingUpstream([]byte("\xff\xd8\xff"))
	defer upstream.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	const maxBytes = 10 * 8192
	h := newTestHandler(dir, upstream).WithMaxCacheBytes(maxBytes)

	for i := range 100 {
		if rr := serveImage(t, h, fmt.Sprintf("%s/%d.jpg", upstream.URL, i)); rr.Code != http.StatusOK {
			t.Fatalf("image %d: status = %d, want 200", i, rr.Code)
		}
	}

	files := cacheFiles(t, cacheDir)
	if files == 0 {
		t.Fatal("nothing was cached")
	}
	if onDisk := files * 4096; onDisk > maxBytes {
		t.Errorf("cache holds %d files, at least %d bytes on disk, over the %d byte cap", files, onDisk, maxBytes)
	}
}

// writeCacheEntry puts one entry (image plus sidecar) on disk, written at mod.
func writeCacheEntry(t *testing.T, cacheDir, name string, mod time.Time) string {
	t.Helper()
	shard := filepath.Join(cacheDir, "ab")
	if err := os.MkdirAll(shard, 0o750); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(shard, name)
	if err := os.WriteFile(img, []byte("\xff\xd8\xffFAKEJPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img+".ct", []byte("image/jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(img, mod, mod); err != nil {
		t.Fatal(err)
	}
	return img
}

func cacheFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// waitFor polls until ok reports true, and fails the test if it never does.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestImageProxy_SweepRemovesOldestWhenFull: a full cache whose entries are
// all inside the age limit used to stay full until they aged out. The sweep
// now removes the oldest entries until the cache is back at nine tenths of
// its cap, and leaves the rest.
func TestImageProxy_SweepRemovesOldestWhenFull(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	now := time.Now()
	var entries []string
	for i := range 10 {
		// Entry 0 is the oldest. All are well inside the 30 day limit.
		entries = append(entries, writeCacheEntry(t, cacheDir, fmt.Sprintf("e%d", i), now.Add(time.Duration(i-20)*time.Hour)))
	}

	h := NewImageProxyHandler(dir).WithMaxCacheBytes(10 * 8192)
	if size, _ := h.CacheSize(); size != 10*8192 {
		t.Fatalf("CacheSize before the sweep = %d, want %d", size, 10*8192)
	}

	h.sweep()

	if cacheFileExists(entries[0]) || cacheFileExists(entries[0]+".ct") {
		t.Error("the oldest entry is still there after the sweep")
	}
	for _, e := range entries[1:] {
		if !cacheFileExists(e) || !cacheFileExists(e+".ct") {
			t.Errorf("%s was removed, only the oldest entry had to go", filepath.Base(e))
		}
	}
	if size, _ := h.CacheSize(); size != 9*8192 {
		t.Errorf("CacheSize after the sweep = %d, want %d", size, 9*8192)
	}

	// Under the target now, so another sweep removes nothing.
	h.sweep()
	if got := cacheFiles(t, cacheDir); got != 18 {
		t.Errorf("a second sweep left %d files, want 18", got)
	}
}

// TestImageProxy_RefusedWriteAsksForSweep: a write refused for the cap asks
// for a sweep straight away, so room comes back without waiting for the
// daily one, and asks at most once an hour.
func TestImageProxy_RefusedWriteAsksForSweep(t *testing.T) {
	upstream, _ := countingUpstream([]byte("\xff\xd8\xffFAKEJPEGBODYBYTES"))
	defer upstream.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	old := writeCacheEntry(t, cacheDir, "old", time.Now().Add(-48*time.Hour))
	writeCacheEntry(t, cacheDir, "newer", time.Now().Add(-time.Hour))

	h := newTestHandler(dir, upstream).WithMaxCacheBytes(2 * 8192)

	if rr := serveImage(t, h, upstream.URL+"/a.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	waitFor(t, "the early sweep to remove the oldest entry", func() bool { return !cacheFileExists(old) })
	// Let the sweep finish its bookkeeping before reading the total.
	h.sweepMu.Lock()
	h.sweepMu.Unlock() //nolint:staticcheck // empty critical section on purpose: it waits for the sweep
	if size, _ := h.CacheSize(); size != 8192 {
		t.Fatalf("CacheSize after the early sweep = %d, want 8192", size)
	}

	// There is room again, so the next image is stored.
	if rr := serveImage(t, h, upstream.URL+"/b.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := cacheFiles(t, cacheDir); got != 4 {
		t.Fatalf("cache holds %d files, want 4 (the newer entry and the one just stored)", got)
	}

	// Full again. This refusal is inside the hour, so no second sweep runs
	// and nothing is removed.
	asked := h.fullSweepAt
	if rr := serveImage(t, h, upstream.URL+"/c.jpg"); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !h.fullSweepAt.Equal(asked) {
		t.Error("a second sweep was asked for inside the hour")
	}
	if got := cacheFiles(t, cacheDir); got != 4 {
		t.Errorf("cache holds %d files after the second refusal, want 4", got)
	}
}

// TestImageProxy_SweepRunsSoonAfterStart: the first sweep used to fire only
// after a full interval, so an instance restarted every day never swept.
func TestImageProxy_SweepRunsSoonAfterStart(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	stale := writeCacheEntry(t, cacheDir, "stale", time.Now().Add(-imageCacheTTL-time.Hour))

	h := NewImageProxyHandler(dir)
	h.startEviction(time.Millisecond, time.Hour)

	waitFor(t, "the startup sweep to remove the expired entry", func() bool { return !cacheFileExists(stale) && !cacheFileExists(stale+".ct") })
}

// TestImageProxy_ConcurrentWritesStayUnderCap: room is claimed before the
// write under one lock, so requests in flight together cannot all pass the
// check against the same total.
func TestImageProxy_ConcurrentWritesStayUnderCap(t *testing.T) {
	upstream, _ := countingUpstream([]byte("\xff\xd8\xffFAKEJPEGBODYBYTES"))
	defer upstream.Close()

	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "image-cache")
	const maxBytes = 2 * 8192
	h := newTestHandler(dir, upstream).WithMaxCacheBytes(maxBytes)
	h.fullSweepAt = time.Now() // no early sweep: this is about the claim alone
	if size, _ := h.CacheSize(); size != 0 {
		t.Fatalf("CacheSize on an empty cache = %d, want 0", size)
	}

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rr := serveImage(t, h, fmt.Sprintf("%s/%d.jpg", upstream.URL, i)); rr.Code != http.StatusOK {
				t.Errorf("image %d: status = %d, want 200", i, rr.Code)
			}
		}()
	}
	wg.Wait()

	if got := cacheFiles(t, cacheDir); got != 4 {
		t.Errorf("cache holds %d files, want 4 (two entries fit under the cap)", got)
	}
	if size, _ := h.CacheSize(); size != maxBytes {
		t.Errorf("CacheSize = %d, want %d", size, maxBytes)
	}
	if h.reservedBytes != 0 {
		t.Errorf("reservedBytes = %d after every write settled, want 0", h.reservedBytes)
	}
}

// TestImageProxy_FailedWriteReleasesItsClaim: a write that cannot land gives
// its claimed room back, so failures do not slowly fill the cap with nothing.
func TestImageProxy_FailedWriteReleasesItsClaim(t *testing.T) {
	upstream, _ := countingUpstream([]byte("\xff\xd8\xffFAKEJPEGBODYBYTES"))
	defer upstream.Close()

	dir := t.TempDir()
	// A plain file where the cache directory belongs, so nothing can be
	// written under it.
	if err := os.WriteFile(filepath.Join(dir, "image-cache"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(dir, upstream)
	// The walk counts the stand-in file itself, so compare against that.
	before, _ := h.CacheSize()

	for i := range 3 {
		if rr := serveImage(t, h, fmt.Sprintf("%s/%d.jpg", upstream.URL, i)); rr.Code != http.StatusOK {
			t.Fatalf("image %d: status = %d, want 200 even though it cannot be cached", i, rr.Code)
		}
	}
	if h.reservedBytes != 0 {
		t.Errorf("reservedBytes = %d after failed writes, want 0", h.reservedBytes)
	}
	if h.sizeBytes != before {
		t.Errorf("sizeBytes = %d after failed writes, want the %d it started at", h.sizeBytes, before)
	}
}
