package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/vavallee/bindery/internal/importer"
)

// trackedFileCache holds the manual-import scan's "already tracked" index —
// the same-path set plus the os.FileInfo values needed for hardlink detection
// (fileMatchesTracked/directoryMatchesTracked) — rebuilt only when book_files
// has actually changed, or the scanned root sits on a different device than
// the index it holds was built for (#2480).
//
// Every scan used to re-stat the entire book_files table regardless of the
// scanned folder's size, which is expensive on network storage (#1473 already
// hit the same handler's write timeout once). Caching the index keyed on the
// table's own fingerprint turns that into "stat the library once per
// book_files mutation, anywhere in the app" instead of "once per scan
// request".
type trackedFileCache struct {
	mu sync.Mutex

	count        int64
	maxID        int64
	pathEpoch    uint64
	rootDevID    uint64
	rootDevKnown bool
	tracked      map[string]struct{}
	trackedFiles *trackedFileSet
}

// trackedFileIndex returns the current already-tracked path set and the
// os.FileInfo values needed for hardlink detection against scanRoot, keyed by
// device and inode (trackedFileSet),
// rebuilding only when book_files has changed since the last build (per
// BookRepo.BookFilesFingerprint for inserts and deletes, and
// BookRepo.BookFilesPathEpoch for the reorganize action's in place path
// rewrites, which the fingerprint cannot see) or scanRoot's device differs from the one
// the cached index was built for.
//
// A tracked path on a different device than scanRoot can never be a hardlink
// of anything under scanRoot, so it skips the expensive directory walk that
// hardlink detection otherwise needs for it — it still gets the cheap
// exact-path entry. This targets network-storage setups where the library and
// a freshly scanned download share live on different mounts (the NFS case
// called out in review on #2480). scanRoot's device is resolved once per
// call, here, rather than once per tracked row: an earlier version compared
// devices via confirmedCrossDevice(scanRoot, trackedPath) inside the rebuild
// loop below, which re-stat'd scanRoot on every single tracked row in the
// library — 3000 tracked rows meant 3000 extra stats of the same path. Now
// each row's device comes off the same os.Stat this loop already does to
// build its FileInfo, so a cold rebuild costs exactly one stat per tracked row
// (plus the directory walk for a genuine tracked folder), not up to five.
func (h *ManualImportHandler) trackedFileIndex(ctx context.Context, scanRoot string) (map[string]struct{}, *trackedFileSet, error) {
	// Read the rename epoch first so a reorganize racing this rebuild can only
	// make the next call rebuild again, never leave a stale path cached.
	pathEpoch := h.books.BookFilesPathEpoch()
	count, maxID, err := h.books.BookFilesFingerprint(ctx)
	if err != nil {
		return nil, nil, err
	}
	rootDevID, rootDevKnown := deviceID(scanRoot)

	h.trackedCache.mu.Lock()
	defer h.trackedCache.mu.Unlock()

	c := &h.trackedCache
	if c.tracked != nil && c.count == count && c.maxID == maxID && c.pathEpoch == pathEpoch && c.rootDevID == rootDevID && c.rootDevKnown == rootDevKnown {
		return c.tracked, c.trackedFiles, nil
	}

	trackedPaths, err := h.books.ListAllBookFilePaths(ctx)
	if err != nil {
		return nil, nil, err
	}
	tracked := make(map[string]struct{}, len(trackedPaths))
	trackedFiles := make([]os.FileInfo, 0, len(trackedPaths))
	dirs := newDirResolver()
	for _, trackedPath := range trackedPaths {
		cleaned := filepath.Clean(trackedPath)
		tracked[cleaned] = struct{}{}
		// Scan walks the symlink-resolved folder (resolveImportFolder), but
		// book_files holds paths in whatever form the library root was
		// configured, so a root reached through a symlink never matched here
		// by path (#2868). Index the resolved form too.
		tracked[dirs.resolvePath(cleaned)] = struct{}{}
		info, statErr := os.Stat(cleaned) //nolint:gosec // #nosec G304 -- path comes from book_files, populated only by prior imports through this same admin-gated handler
		if statErr != nil {
			continue
		}
		if rootDevKnown {
			if pathDevID, ok := deviceIDOf(info); ok && pathDevID != rootDevID {
				continue
			}
		}
		trackedFiles = append(trackedFiles, info)
		if !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(cleaned, func(p string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || (!importer.IsEbookFile(p) && !importer.IsAudioFile(p)) {
				return nil
			}
			if childInfo, childErr := os.Stat(p); childErr == nil { //nolint:gosec // #nosec G304 -- p is a directory entry beneath a book_files path, same trust boundary as above
				trackedFiles = append(trackedFiles, childInfo)
			}
			return nil
		})
	}

	c.count = count
	c.maxID = maxID
	c.pathEpoch = pathEpoch
	c.rootDevID = rootDevID
	c.rootDevKnown = rootDevKnown
	c.tracked = tracked
	c.trackedFiles = newTrackedFileSet(trackedFiles)
	return tracked, c.trackedFiles, nil
}

// dirResolver maps a path to the form Scan sees after resolveImportFolder's
// symlink resolution, so book_files rows written under a symlinked library
// root still match the scanned units by exact path (#2868). Only directory
// components are resolved; the leaf is kept as-is because the scan walk
// reports a symlinked file under its own name, not its target's.
//
// filepath.EvalSymlinks per tracked row would cost an lstat per component per
// row on a cold rebuild, and fails outright for a path that is missing at
// rebuild time, which is exactly the case the exact-path entry exists for (a
// file on an unmounted share has no FileInfo to hardlink-match against).
// Resolving one component at a time with a per-directory memo costs one lstat
// per distinct directory in the library and still resolves the existing
// prefix of a missing path.
type dirResolver struct {
	memo map[string]string
}

func newDirResolver() *dirResolver {
	return &dirResolver{memo: make(map[string]string)}
}

func (r *dirResolver) resolvePath(p string) string {
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(r.resolveDir(dir), filepath.Base(p))
}

func (r *dirResolver) resolveDir(dir string) string {
	if out, ok := r.memo[dir]; ok {
		return out
	}
	out := dir
	if parent := filepath.Dir(dir); parent != dir {
		out = filepath.Join(r.resolveDir(parent), filepath.Base(dir))
		if fi, err := os.Lstat(out); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if real, err := filepath.EvalSymlinks(out); err == nil {
				out = real
			}
		}
	}
	r.memo[dir] = out
	return out
}
