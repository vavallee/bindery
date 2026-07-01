package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

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
// wiring in cmd/bindery/main.go always supplies a non-nil instance.
type LibraryRoots struct {
	lister   RootLister
	defaults []string
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

// Contains reports whether p is inside at least one configured library root.
// The check is deliberately strict:
//
//   - Empty or relative inputs are rejected. The importer always writes
//     absolute paths; a relative path in a `file_path` column is a bug or a
//     hostile payload, and there is no safe way to anchor it.
//   - p and each root are filepath.Cleaned before comparison so trailing
//     slashes and `..` segments don't bypass the prefix check.
//   - Symlinks inside p are resolved via filepath.EvalSymlinks when possible.
//     If the path no longer exists, EvalSymlinks fails — we fall back to the
//     lexical Clean+Rel check rather than refusing the delete (the file is
//     already gone or the symlink target moved; either way refusing here
//     would just orphan the DB row indefinitely).
//   - Containment is established via filepath.Rel: the relative path from
//     the root to p must not start with ".." and must not be "." (a root
//     pointed at itself is not a valid book file).
//
// Returns true iff at least one root contains p. A nil receiver returns
// true — the caller has opted out of the check.
func (r *LibraryRoots) Contains(ctx context.Context, p string) bool {
	if r == nil {
		return true
	}
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	cleaned := filepath.Clean(p)
	// Best-effort symlink resolution. A failure here (file deleted, broken
	// link, permission denied) just falls back to the lexical check below.
	resolved := cleaned
	if real, err := filepath.EvalSymlinks(cleaned); err == nil {
		resolved = real
	}
	roots := r.resolveRoots(ctx)
	if len(roots) == 0 {
		// No roots configured at all (no DB rows AND no static defaults).
		// We can't tell what's "inside"; allow to preserve legacy behaviour
		// for single-tenant installs that never wired roots. Production
		// wiring always seeds at least one default.
		return true
	}
	for _, root := range roots {
		if containsUnderRoot(resolved, root) {
			return true
		}
		// Also try the un-resolved form: symlinks pointing into the library
		// from outside should still pass when the lexical path is inside.
		if resolved != cleaned && containsUnderRoot(cleaned, root) {
			return true
		}
	}
	return false
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
	if r == nil {
		return filepath.Clean(p), true
	}
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		// Broken link, missing file, or permission denied: can't prove
		// containment, so refuse. (Contains is lenient here for the delete
		// path, where the input is a trusted DB row rather than a
		// user-supplied path that might be a symlink out of the library.)
		return "", false
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
		if containsUnderRoot(resolved, realRoot) {
			return resolved, true
		}
	}
	return "", false
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
	if owner != nil {
		otherOwns, ownErr := owner.PathOwnedByOtherBook(ctx, p, excludeBookID)
		if ownErr != nil {
			fields := append([]any{"path", p, "operation", "book_file_delete", "error", ownErr}, logFields...)
			slog.Warn("path ownership: could not verify book_files ownership; skipping disk delete", fields...)
			return true, nil
		}
		if otherOwns {
			fields := append([]any{"path", p, "operation", "book_file_delete"}, logFields...)
			slog.Warn("path ownership: file still tracked by another book; skipping disk delete", fields...)
			return true, nil
		}
	}
	return false, removeBookPathScoped(p, format)
}

// bookFileOwner reports whether an on-disk path is still registered in
// book_files to a book other than excludeBookID. safeRemoveBookPath consults it
// so no delete path — book delete, per-file delete, author delete, reassign
// cleanup — can unlink a file another book still owns (#1368). The book_files
// path column is globally UNIQUE, so any hit is a single unambiguous owner.
type bookFileOwner interface {
	PathOwnedByOtherBook(ctx context.Context, path string, excludeBookID int64) (bool, error)
}
