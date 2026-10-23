package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vavallee/bindery/internal/models"
)

// RootLister is the minimal surface LibraryRoots needs from the root-folders
// repo. Declared here (rather than depending on *db.RootFolderRepo directly)
// so tests can plug in a static list without spinning up a database.
type RootLister interface {
	List(ctx context.Context) ([]models.RootFolder, error)
}

// LibraryRoots resolves the set of on-disk paths that the user has declared
// "Bindery may write to and delete from". It backs the defence-in-depth
// containment check used by the delete handlers (Wave 1 / Bundle B): even if
// a DB row ever ends up with a `file_path` outside any configured library
// (via a future bug, a tampered import, or a hostile metadata payload), the
// delete handler refuses to walk outside the configured roots.
//
// Roots come from two places:
//
//   - The `root_folders` table, populated by the user from the UI. This is the
//     dynamic, primary source of truth.
//   - A static `defaults` slice carried by the process — typically the
//     BINDERY_LIBRARY_DIR / BINDERY_AUDIOBOOK_DIR env vars. These cover the
//     legacy single-root setup where the user never created a root_folders
//     row but the importer still writes under one of those dirs.
//
// Both are merged into a deduplicated, filepath.Cleaned set per call. The
// lookup is small (typically <5 roots) so we don't bother caching.
//
// A nil *LibraryRoots is a deliberate signal: "no containment check
// available". Callers treat nil as "allow", preserving backwards behaviour
// for tests and any code path that hasn't been wired yet. The production
// wiring in cmd/bindery/main.go always supplies a non-nil instance seeded with
// BINDERY_LIBRARY_DIR (default /books). A non-nil instance with no roots
// resolving refuses everything.
type LibraryRoots struct {
	lister   RootLister
	defaults []string

	noRootsOnce sync.Once // one WARN when Contains finds zero roots
}

// NewLibraryRoots builds a LibraryRoots backed by the given lister and any
// static fallback paths (typically BINDERY_LIBRARY_DIR and
// BINDERY_AUDIOBOOK_DIR). nil or empty default entries are dropped silently.
func NewLibraryRoots(lister RootLister, defaults ...string) *LibraryRoots {
	clean := make([]string, 0, len(defaults))
	for _, d := range defaults {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		clean = append(clean, filepath.Clean(d))
	}
	return &LibraryRoots{lister: lister, defaults: clean}
}

// resolveRoots returns the merged + cleaned set of root paths for this call.
// Errors from the DB lister are logged and treated as "no DB roots" so a
// transient DB hiccup does not turn the containment check into a deny-all
// (which would block legitimate file deletions). The static defaults still
// apply.
func (r *LibraryRoots) resolveRoots(ctx context.Context) []string {
	seen := make(map[string]struct{}, len(r.defaults)+4)
	out := make([]string, 0, len(r.defaults)+4)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, d := range r.defaults {
		add(d)
	}
	if r.lister != nil {
		folders, err := r.lister.List(ctx)
		if err != nil {
			slog.Warn("path containment: failed to list root folders, falling back to static defaults", "error", err)
		} else {
			for _, f := range folders {
				add(f.Path)
			}
		}
	}
	return out
}

// Configured returns the resolved library roots, for handlers that need to tell
// the caller what a valid path looks like after refusing one. It is the
// exported read-only view of resolveRoots; a nil receiver returns nil (the
// containment check is opted out, so there is nothing to name).
//
// The result is the operator's own root folder configuration and nothing else.
// Only quote it back on admin-only endpoints.
func (r *LibraryRoots) Configured(ctx context.Context) []string {
	if r == nil {
		return nil
	}
	return r.resolveRoots(ctx)
}

// Contains reports whether p is inside at least one configured library root.
// The check is deliberately strict:
//
//   - Empty or relative inputs are rejected. The importer always writes
//     absolute paths; a relative path in a `file_path` column is a bug or a
//     hostile payload, and there is no safe way to anchor it.
//   - p and each root are filepath.Cleaned and symlink-resolved before
//     comparison, so trailing slashes, `..` segments and symlinks cannot
//     bypass the prefix check. A root that is itself a symlink (a library on
//     another volume) is resolved too, so it keeps matching.
//   - When p does not exist (the file is already gone) the deepest existing
//     ancestor is resolved and the missing tail appended, so a stale row for a
//     deleted file still passes while a missing leaf under a directory symlink
//     that escapes the root does not. There is no lexical fallback: a symlink
//     that sits inside a root but resolves outside every root is refused.
//   - Containment is established via filepath.Rel: the relative path from
//     the root to p must not start with ".." and must not be "." (a root
//     pointed at itself is not a valid book file).
//   - When no root resolves at all (no DB rows, no static defaults, or the
//     lister failed with no defaults) nothing can be "inside" and the answer
//     is false, with a one time WARN naming the cause. Production wiring
//     always seeds BINDERY_LIBRARY_DIR (default /books), so this is only
//     reachable if that wiring is removed.
//
// Returns true iff at least one root contains p. A nil receiver returns
// true: the caller has opted out of the check.
func (r *LibraryRoots) Contains(ctx context.Context, p string) bool {
	if r == nil {
		return true
	}
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	roots := r.resolveRoots(ctx)
	if len(roots) == 0 {
		r.noRootsOnce.Do(func() {
			slog.Warn("path containment: no library roots configured (no root folders and no BINDERY_LIBRARY_DIR / BINDERY_AUDIOBOOK_DIR); refusing every file delete until one is set")
		})
		return false
	}
	// A ".." segment is refused outright. Clean folds "a/link/.." to "a", but
	// the kernel resolves the link first, so the path the delete would use
	// ("a/link/../x" is the parent of the link target) is not the one checked
	// here. The importer never writes one.
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return false
		}
	}
	resolved, ok := resolveWithMissingTail(filepath.Clean(p))
	if !ok {
		return false
	}
	for _, root := range roots {
		if containsUnderRoot(resolved, root) {
			return true
		}
		if realRoot, rok := resolveWithMissingTail(root); rok && realRoot != root && containsUnderRoot(resolved, realRoot) {
			return true
		}
	}
	return false
}

// resolveWithMissingTail resolves symlinks in p. If p (or a trailing part of
// it) does not exist, the deepest existing ancestor is resolved and the
// missing tail appended unchanged, so the result is the path the filesystem
// would actually use. p must be absolute and Cleaned.
//
// ok is false when a component exists but cannot be resolved: a dangling
// symlink or a symlink loop. EvalSymlinks fails for those exactly as it does
// for a missing name, and treating them as missing would accept a path that
// runs through a link whose target is not there yet (or never will be
// resolvable here), so the caller must refuse it.
func resolveWithMissingTail(p string) (string, bool) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, true
	}
	if _, err := os.Lstat(p); err == nil {
		return "", false
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, true
	}
	head, ok := resolveWithMissingTail(parent)
	if !ok {
		return "", false
	}
	return filepath.Join(head, filepath.Base(p)), true
}

// ResolveContained resolves p by following symlinks and returns the resolved
// absolute path only if it lies within a configured root. It is the strict
// counterpart to Contains, intended for the manual-import handlers where the
// path is read from, copied from, or moved: a symlink that physically sits
// inside a library root but points outside it must NOT be allowed, or it
// becomes an arbitrary-file read/move primitive.
//
// Unlike Contains there is no lexical fallback — only the symlink-resolved path
// is checked, and the roots are resolved too so a symlinked root directory
// still matches. The path must exist (filepath.EvalSymlinks must succeed); a
// path that cannot be resolved is rejected (fail-closed). Callers must operate
// on the returned resolved path so the containment check and the subsequent
// file operation act on the same bytes.
//
// A nil receiver opts out (returns the cleaned path, true), mirroring Contains
// and preserving behaviour for callers/tests that have not wired roots.
func (r *LibraryRoots) ResolveContained(ctx context.Context, p string) (string, bool) {
	resolved, res := r.CheckContained(ctx, p)
	return resolved, res == ContainOK
}

// ContainResult says why CheckContained accepted or refused a path.
type ContainResult int

const (
	// ContainOK: the path resolved inside a configured root.
	ContainOK ContainResult = iota
	// ContainOutside: the path is not under any root, or it resolves outside
	// every root (a symlink or ".." out). This is the only refusal that means
	// "outside the library".
	ContainOutside
	// ContainUnreadable: the path resolves inside a root but cannot be
	// resolved (missing, renamed, unmounted, dangling link, permission
	// denied), so containment could not be proven. It is refused exactly like
	// ContainOutside; only the explanation differs.
	ContainUnreadable
)

// CheckContained is ResolveContained with the reason for a refusal. The accept
// decision is identical: ok if and only if ResolveContained would accept. The
// extra detail only separates "outside the library" from "inside the library
// on paper but unreadable", so callers can word the error truthfully.
func (r *LibraryRoots) CheckContained(ctx context.Context, p string) (string, ContainResult) {
	if r == nil {
		return filepath.Clean(p), ContainOK
	}
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return "", ContainOutside
	}
	clean := filepath.Clean(p)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		// Broken link, missing file, or permission denied: can't prove
		// containment, so refuse. (Contains is lenient here for the delete
		// path, where the input is a trusted DB row rather than a
		// user-supplied path that might be a symlink out of the library.)
		// Whether it resolves inside a root decides only the wording.
		if r.resolvesInRoot(ctx, clean) {
			return "", ContainUnreadable
		}
		return "", ContainOutside
	}
	// Fail closed when no roots are configured: an admin-triggered file
	// read/move outside any library is too dangerous to allow-all. (Contains
	// falls open in this case for the delete path's legacy single-tenant
	// installs; the manual-import path must not.)
	for _, root := range r.resolveRoots(ctx) {
		realRoot := root
		if rr, err := filepath.EvalSymlinks(root); err == nil {
			realRoot = rr
		}
		// Import/scan may legitimately target a configured root itself (e.g.
		// "scan everything under /books"), unlike the delete path where a root
		// is never a deletable book. Allow root-equality here (#1373).
		if containsUnderOrEqualRoot(resolved, realRoot) {
			return resolved, ContainOK
		}
	}
	return "", ContainOutside
}

// resolvesInRoot reports whether the cleaned absolute path p, once its deepest
// existing ancestor is resolved through every symlink and the missing tail is
// appended, lands under (or at) a configured root. It is used only to pick the
// wording of a refusal and never to accept a path. It deliberately ignores how
// p was typed: a path that runs through a link pointing outside every root
// resolves outside, so it must be indistinguishable from any other outside
// path, whether or not the file exists there.
func (r *LibraryRoots) resolvesInRoot(ctx context.Context, p string) bool {
	real, ok := r.resolveRootPrefix(ctx, p)
	return ok && r.underAnyRoot(ctx, real)
}

// resolveRootPrefix resolves the deepest existing ancestor of p through
// every symlink and appends the missing tail. It reports false when it cannot
// decide: a name that exists but does not resolve (a dangling link, a link
// loop) is not followed. Following one would mean joining its target onto a
// directory and cleaning the result, and ".." in a target applies after the
// kernel resolves the component before it, so string arithmetic would put the
// file somewhere else. Such a path is refused as outside, the same answer
// every other undecidable path gets.
func (r *LibraryRoots) resolveRootPrefix(ctx context.Context, p string) (string, bool) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, true
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, true
	}
	base := filepath.Base(p)
	head, err := filepath.EvalSymlinks(parent)
	if err != nil {
		head, ok := r.resolveRootPrefix(ctx, parent)
		if !ok {
			return "", false
		}
		return filepath.Join(head, base), true
	}
	// The parent resolved, so look the name up among its entries. The listing
	// uses the resolved parent and only compares names, so no raw path reaches a
	// filesystem call. A name that is listed yet did not resolve is a dangling
	// link or a loop. An unreadable parent lists nothing, so its children count
	// as missing, as they would for any caller that cannot see them.
	//
	// Only a parent that resolves inside a configured root is listed. Anywhere
	// else the answer is "outside" whatever the listing would say, so a
	// directory outside the library is never read at all.
	if !r.underAnyRoot(ctx, head) {
		return filepath.Join(head, base), true
	}
	if entries, err := readDirForWording(head); err == nil {
		for _, e := range entries {
			if e.Name() == base {
				return "", false
			}
		}
	}
	return filepath.Join(head, base), true
}

// readDirForWording is os.ReadDir behind a seam so a test can prove which
// directories the wording check reads.
var readDirForWording = os.ReadDir

func (r *LibraryRoots) underAnyRoot(ctx context.Context, p string) bool {
	for _, root := range r.resolveRoots(ctx) {
		if containsUnderOrEqualRoot(p, root) {
			return true
		}
		if rr, err := filepath.EvalSymlinks(root); err == nil && containsUnderOrEqualRoot(p, rr) {
			return true
		}
	}
	return false
}

// containsUnderOrEqualRoot is containsUnderRoot but also accepts p == root. Used
// by the manual-import read/scan path (ResolveContained), where pointing at a
// configured directory as a whole is a valid operation (#1373).
func containsUnderOrEqualRoot(p, root string) bool {
	if root == "" || root == "." {
		return false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true // rel == "." (p == root) is allowed here
}

// containsUnderRoot is the lexical containment primitive shared by Contains.
// Returns true iff p is the same as root or strictly nested under it.
func containsUnderRoot(p, root string) bool {
	if root == "" || root == "." {
		return false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		// p == root. A library root itself is not a deletable book path.
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// safeRemoveBookPath is the containment-gated wrapper around
// removeBookPathScoped used by the book/author delete handlers. If roots is
// non-nil and p is not contained, the deletion is skipped with a WARN log
// and (skipped=true, nil) is returned: skipped=true signals "we
// intentionally did not touch disk".
//
// Calibre-imported paths get the same containment check. Letting calibre
// rows allow-list themselves around the gate would re-introduce the exact
// failure mode the audit flagged: a hostile or buggy import (e.g. a
// metadata.opf with `<path>/etc/passwd</path>`) could redirect a delete
// outside any library. The Calibre integration writes into a configured
// library dir anyway, so legitimate Calibre books still pass the check.
func safeRemoveBookPath(ctx context.Context, roots *LibraryRoots, owner bookFileOwner, excludeBookID int64, p, format string, logFields ...any) (skipped bool, err error) {
	if !roots.Contains(ctx, p) {
		fields := append([]any{"path", p, "operation", "book_file_delete"}, logFields...)
		slog.Warn("path containment: refusing to delete file outside configured library roots", fields...)
		return true, nil
	}
	// Ownership guard (#1368): never unlink a file that another book still tracks
	// in book_files. A stale legacy path column, a mis-detached Fix Match, or a
	// duplicate record must not let deleting one book destroy another book's
	// file. Fail safe — if ownership can't be determined, skip the disk delete
	// (the DB row is going away regardless; a stranded path is recoverable, a
	// deleted file is not).
	//
	// The same predicate is handed to removeBookPathScoped so it applies to
	// every file that sweep reaches — same-stem siblings, and the contents of an
	// audiobook folder — not only the tracked path passed in here. Checking p
	// alone left the children of a directory delete unguarded (#2052).
	ownedByOther := func(path string) bool {
		if owner == nil {
			return false
		}
		otherOwns, ownErr := pathGuardedByOtherBook(ctx, owner, excludeBookID, path)
		if ownErr != nil {
			fields := append([]any{"path", path, "operation", "book_file_delete", "error", ownErr}, logFields...)
			slog.Warn("path ownership: could not verify book_files ownership; skipping disk delete", fields...)
			return true
		}
		if otherOwns {
			fields := append([]any{"path", path, "operation", "book_file_delete"}, logFields...)
			slog.Warn("path ownership: file still tracked by another book; skipping disk delete", fields...)
			return true
		}
		return false
	}
	if ownedByOther(p) {
		return true, nil
	}
	return false, removeBookPathScoped(p, format, ownedByOther)
}

// safeRemoveBookPathExact is the single-row variant of safeRemoveBookPath.
// Unlike the format-scoped delete, it never sweeps same-stem siblings. A
// directory remains a unit because audiobook book_files rows represent the
// complete bundle, but files beside a targeted ebook are left untouched.
func safeRemoveBookPathExact(ctx context.Context, roots *LibraryRoots, owner bookFileOwner, excludeBookID int64, p, format string, logFields ...any) (skipped bool, err error) {
	if !roots.Contains(ctx, p) {
		fields := append([]any{"path", p, "operation", "book_file_delete"}, logFields...)
		slog.Warn("path containment: refusing to delete file outside configured library roots", fields...)
		return true, nil
	}
	ownedByOther := func(path string) bool {
		if owner == nil {
			return false
		}
		otherOwns, ownErr := pathGuardedByOtherBook(ctx, owner, excludeBookID, path)
		if ownErr != nil {
			fields := append([]any{"path", path, "operation", "book_file_delete", "error", ownErr}, logFields...)
			slog.Warn("path ownership: could not verify book_files ownership; skipping disk delete", fields...)
			return true
		}
		return otherOwns
	}
	if ownedByOther(p) {
		return true, nil
	}
	info, statErr := os.Stat(p)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}
	if info.IsDir() {
		return false, removeBookDirScoped(p, format, ownedByOther)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) { // #nosec G304 G703 -- p is gated by the configured library roots above
		return false, err
	}
	// Prune the parent directory if it is now empty, mirroring the same
	// cleanup removeBookPathScoped does for the format-scoped sweep. The
	// exact delete never sweeps siblings, but a folder this deletion leaves
	// empty should not linger any more than the scoped sweep's does.
	parent := filepath.Dir(p)
	if remaining, readErr := os.ReadDir(parent); readErr == nil && len(remaining) == 0 {
		_ = os.Remove(parent) // #nosec G304 G703 -- parent = filepath.Dir of a path already gated by the configured library roots above
	}
	return false, nil
}

// bookFileOwner reports whether an on-disk path is still registered in
// book_files to a book other than excludeBookID. safeRemoveBookPath consults it
// so no delete path — book delete, per-file delete, author delete, reassign
// cleanup — can unlink a file another book still owns (#1368). The book_files
// path column is globally UNIQUE, so any hit is a single unambiguous owner.
type bookFileOwner interface {
	PathOwnedByOtherBook(ctx context.Context, path string, excludeBookID int64) (bool, error)
	// PathUnderOtherBookDir reports whether a book other than
	// excludeBookID tracks a folder containing path (#3336).
	PathUnderOtherBookDir(ctx context.Context, path string, excludeBookID int64) (bool, error)
}

// pathGuardedByOtherBook is the single ownership predicate every delete sweep
// uses: the path is another book's row, or sits under a folder another
// book tracks. A folder row owns what is below it even when no row names the
// file exactly, so an exact-path check alone let a sweep through a box set's
// folder take files from a nested book's folder (#3336).
func pathGuardedByOtherBook(ctx context.Context, owner bookFileOwner, excludeBookID int64, path string) (bool, error) {
	if owned, err := owner.PathOwnedByOtherBook(ctx, path, excludeBookID); err != nil || owned {
		return owned, err
	}
	return owner.PathUnderOtherBookDir(ctx, path, excludeBookID)
}
