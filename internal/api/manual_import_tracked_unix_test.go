//go:build !windows

package api

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// refFileMatchesTracked is fileMatchesTracked as it was before
// trackedFileSet: every tracked FileInfo compared in turn.
func refFileMatchesTracked(path string, tracked []os.FileInfo) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, trackedInfo := range tracked {
		if os.SameFile(info, trackedInfo) &&
			info.Size() == trackedInfo.Size() &&
			info.ModTime().Equal(trackedInfo.ModTime()) {
			return true
		}
	}
	return false
}

func statAll(t *testing.T, paths ...string) []os.FileInfo {
	t.Helper()
	out := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = fi
	}
	return out
}

// TestTrackedFileSet_Hardlinks covers what the device and inode key has to
// get right: a hardlink of a tracked file matches, a copy with the same bytes
// does not, and a tracked file rewritten since the cache was built no longer
// vouches for its hardlink (its mtime moved), as before.
func TestTrackedFileSet_Hardlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracked := filepath.Join(root, "library", "book.epub")
	link := filepath.Join(root, "downloads", "book.epub")
	link2 := filepath.Join(root, "downloads", "again", "book.epub")
	copied := filepath.Join(root, "downloads", "copy.epub")
	other := filepath.Join(root, "downloads", "other.epub")
	stale := filepath.Join(root, "library", "stale.epub")
	staleLink := filepath.Join(root, "downloads", "stale.epub")
	for _, p := range []string{tracked, copied, other, stale} {
		writeTestFile(t, p)
	}
	if err := os.MkdirAll(filepath.Dir(link2), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{{tracked, link}, {link, link2}, {stale, staleLink}} {
		if err := os.Link(l[0], l[1]); err != nil {
			t.Skipf("hardlinks unsupported here: %v", err)
		}
	}
	infos := statAll(t, tracked, stale)
	// Rewrite stale in place after the cache captured it: same inode, new
	// mtime, so the cached FileInfo no longer describes it.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(stale, later, later); err != nil {
		t.Fatal(err)
	}

	set := newTrackedFileSet(infos)
	for path, want := range map[string]bool{
		tracked:                             true,
		link:                                true,
		link2:                               true,
		copied:                              false,
		other:                               false,
		stale:                               false,
		staleLink:                           false,
		filepath.Join(root, "missing.epub"): false,
	} {
		if got := fileMatchesTracked(path, set); got != want {
			t.Errorf("fileMatchesTracked(%s) = %v, want %v", path, got, want)
		}
		if ref := refFileMatchesTracked(path, infos); ref != want {
			t.Errorf("reference fileMatchesTracked(%s) = %v, want %v: the test's expectation is wrong", path, ref, want)
		}
	}
}

// fakeInfo is an os.FileInfo carrying a chosen device and inode, to place
// FileInfos in trackedFileSet buckets without real files on two devices.
type fakeInfo struct {
	os.FileInfo
	sys any
}

func (f fakeInfo) Sys() any { return f.sys }

// TestTrackedFileSet_DeviceInKey simulates the same inode number on two
// devices: they must land in different buckets, so a file on one device is
// never compared with the other's. A FileInfo with no device and inode goes
// to rest and is compared with every query, and a query with none is
// compared with everything, which is what os.SameFile needs on a platform
// where the key is unavailable.
func TestTrackedFileSet_DeviceInKey(t *testing.T) {
	t.Parallel()
	devA := fakeInfo{sys: &syscall.Stat_t{Dev: 1, Ino: 7}}
	devB := fakeInfo{sys: &syscall.Stat_t{Dev: 2, Ino: 7}}
	noKey := fakeInfo{sys: nil}
	set := newTrackedFileSet([]os.FileInfo{devA, devB, noKey})

	if len(set.byIdentity) != 2 {
		t.Fatalf("byIdentity has %d buckets, want 2 (device must be part of the key)", len(set.byIdentity))
	}
	bucket, rest := set.candidates(fakeInfo{sys: &syscall.Stat_t{Dev: 2, Ino: 7}})
	if len(bucket) != 1 || bucket[0] != os.FileInfo(devB) {
		t.Fatalf("candidates for dev 2 inode 7 = %v, want only the dev 2 FileInfo", bucket)
	}
	if len(rest) != 1 || rest[0] != os.FileInfo(noKey) {
		t.Fatalf("rest = %v, want the FileInfo without an identity", rest)
	}
	if bucket, _ := set.candidates(fakeInfo{sys: &syscall.Stat_t{Dev: 3, Ino: 7}}); len(bucket) != 0 {
		t.Fatalf("candidates for dev 3 inode 7 = %v, want none", bucket)
	}
	if all, _ := set.candidates(noKey); len(all) != 3 {
		t.Fatalf("candidates for a query with no identity = %d FileInfos, want all 3", len(all))
	}
	var nilSet *trackedFileSet
	if nilSet.matches(devA) {
		t.Fatal("a nil trackedFileSet matched")
	}
}

// TestTrackedFileSet_RealSecondDevice checks a real file on another device
// (a tmpfs, when the host has one apart from the test temp dir) against a
// tracked set built on the temp dir's device.
func TestTrackedFileSet_RealSecondDevice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	shm, err := os.MkdirTemp("/dev/shm", "bindery-tracked-")
	if err != nil {
		t.Skipf("no /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })
	devRoot, ok1 := deviceID(root)
	devShm, ok2 := deviceID(shm)
	if !ok1 || !ok2 || devRoot == devShm {
		t.Skip("/dev/shm is not a separate device here")
	}
	var infos []os.FileInfo
	for i := range 20 {
		p := filepath.Join(root, fmt.Sprintf("t%d.epub", i))
		writeTestFile(t, p)
		infos = append(infos, statAll(t, p)...)
	}
	set := newTrackedFileSet(infos)
	for i := range 20 {
		p := filepath.Join(shm, fmt.Sprintf("s%d.epub", i))
		writeTestFile(t, p)
		if got, want := fileMatchesTracked(p, set), refFileMatchesTracked(p, infos); got != want || got {
			t.Errorf("fileMatchesTracked(%s) = %v, reference %v, want false", p, got, want)
		}
	}
}

// TestTrackedFileSet_MatchesReference builds a tree of files, hardlinks and
// copies, tracks a random subset, and compares fileMatchesTracked with the
// old linear loop on every file.
func TestTrackedFileSet_MatchesReference(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rng := rand.New(rand.NewSource(3)) //nolint:gosec // G404: deterministic test data
	var all []string
	for i := range 60 {
		p := filepath.Join(root, fmt.Sprintf("d%d", i%5), fmt.Sprintf("f%d.epub", i))
		switch {
		case i > 0 && i%3 == 0:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(all[rng.Intn(len(all))], p); err != nil {
				t.Skipf("hardlinks unsupported here: %v", err)
			}
		default:
			writeTestFile(t, p)
		}
		all = append(all, p)
	}
	var tracked []string
	for _, p := range all {
		if rng.Intn(3) == 0 {
			tracked = append(tracked, p)
		}
	}
	infos := statAll(t, tracked...)
	set := newTrackedFileSet(infos)
	matched := 0
	for _, p := range all {
		want := refFileMatchesTracked(p, infos)
		if want {
			matched++
		}
		if got := fileMatchesTracked(p, set); got != want {
			t.Errorf("fileMatchesTracked(%s) = %v, reference %v", p, got, want)
		}
	}
	if matched <= len(tracked) {
		t.Fatalf("only %d of %d files matched with %d tracked: no hardlink was exercised", matched, len(all), len(tracked))
	}
}
