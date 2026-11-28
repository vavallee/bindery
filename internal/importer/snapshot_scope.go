package importer

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// SnapshotScope shares one LibrarySnapshot per library configuration across
// every FindExisting lookup of a batch: one Refresh all run, one refresh
// selected run, one series fill or series check. Without it each author sync
// (and each book a series fill creates) built its own snapshot and walked the
// whole library again, so Refresh all over 2,000 authors walked the tree up to
// 2,000 times.
//
// The rule, stated: a lookup in the batch sees the library as the first
// lookup's walk saw it, plus the files Bindery itself tracked since.
//
//   - A file that appears on disk outside Bindery after the walk (copied in by
//     hand, written by another tool) is not seen until the batch ends; the
//     next sync or batch sees it. That is the window the per sync snapshot
//     already had, widened from one author to the batch. It never hides a file
//     a walk at the start of the batch would have found, because the walk
//     runs at or after the start.
//   - A file Bindery tracks in book_files after the walk (an import finishing
//     mid batch, a library scan) is seen by the next author sync, as it is
//     today: Share compares the book_files rows added since it last looked
//     against the paths the walk visited, and walks again when one is new to
//     it. A book an author sync binds to a file the snapshot already holds
//     adds a row too, and does not cause a walk.
//   - A reorganize that rewrites paths in place (BookFilesPathEpoch) also
//     starts a fresh walk.
//   - A file removed since the walk is never offered: LibrarySnapshot checks
//     its answer on disk.
//
// A scope is safe for concurrent use; refresh selected runs its syncs in
// parallel.
type SnapshotScope struct {
	mu    sync.Mutex
	snaps map[string]*sharedSnapshot
}

// sharedSnapshot is one snapshot in a scope with the book_files position it
// was last reconciled against.
type sharedSnapshot struct {
	snap *LibrarySnapshot
	// lastID is the highest book_files id whose path the snapshot accounts
	// for. epoch is BookFilesPathEpoch when the snapshot was handed out.
	lastID int64
	epoch  uint64
}

// TrackedFiles is what a scope reads from book_files to notice files Bindery
// tracked after a walk. Implemented by *db.BookRepo.
type TrackedFiles interface {
	BookFilesFingerprint(ctx context.Context) (count, maxID int64, err error)
	BookFilesPathEpoch() uint64
	// BookFilePathsAfter returns the paths of the book_files rows with an id
	// above afterID and the highest such id (afterID when there are none).
	BookFilePathsAfter(ctx context.Context, afterID int64) (paths []string, maxID int64, err error)
}

type snapshotScopeKey struct{}

// WithSnapshotScope returns ctx carrying a SnapshotScope. When ctx already
// carries one it is returned unchanged, so a batch nested in a batch shares
// the outer one.
func WithSnapshotScope(ctx context.Context) context.Context {
	if SnapshotScopeFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, snapshotScopeKey{}, &SnapshotScope{})
}

// ContextWithSnapshotScope returns ctx carrying scope (ctx unchanged for a
// nil scope). It moves a batch's scope onto another context, such as the
// lifetime context a sync runs on.
func ContextWithSnapshotScope(ctx context.Context, scope *SnapshotScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, snapshotScopeKey{}, scope)
}

// SnapshotScopeFrom returns the scope ctx carries, or nil.
func SnapshotScopeFrom(ctx context.Context) *SnapshotScope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(snapshotScopeKey{}).(*SnapshotScope)
	return s
}

// snapshotKey identifies a library configuration: a snapshot is shared only
// with lookups over the same roots and exclusions, so a settings change mid
// batch gets its own walk.
func (ls *LibrarySnapshot) snapshotKey() string {
	return ls.libraryDir + "\x00" + ls.audiobookDir + "\x00" + strings.Join(ls.excluded, "\x00")
}

// Share returns the scope's snapshot for fresh's library configuration,
// storing fresh when there is none or when the held one is out of date (see
// SnapshotScope). fresh must not have been queried yet. tracked nil, or an
// error reading it, keeps the per call behaviour: fresh is returned and not
// shared, so a failure costs walks, never a missed file.
func (s *SnapshotScope) Share(ctx context.Context, fresh *LibrarySnapshot, tracked TrackedFiles) *LibrarySnapshot {
	if s == nil || fresh == nil || tracked == nil {
		return fresh
	}
	key := fresh.snapshotKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if held := s.snaps[key]; held != nil {
		if held.current(ctx, tracked) {
			return held.snap
		}
		delete(s.snaps, key)
	}
	// The epoch is read before the rows, so a rewrite racing this read
	// shows up as a changed epoch next time and errs toward a walk.
	epoch := tracked.BookFilesPathEpoch()
	_, maxID, err := tracked.BookFilesFingerprint(ctx)
	if err != nil {
		slog.Warn("library: book_files position unreadable, not sharing the library snapshot", "error", err)
		return fresh
	}
	if s.snaps == nil {
		s.snaps = make(map[string]*sharedSnapshot)
	}
	s.snaps[key] = &sharedSnapshot{snap: fresh, lastID: maxID, epoch: epoch}
	return fresh
}

// current reports whether the held snapshot still answers as the rule says,
// advancing lastID past the rows it accounts for.
func (sh *sharedSnapshot) current(ctx context.Context, tracked TrackedFiles) bool {
	if tracked.BookFilesPathEpoch() != sh.epoch {
		slog.Debug("library: tracked paths were rewritten, walking the library again")
		return false
	}
	paths, maxID, err := tracked.BookFilePathsAfter(ctx, sh.lastID)
	if err != nil {
		slog.Warn("library: could not read newly tracked files, walking the library again", "error", err)
		return false
	}
	for _, p := range paths {
		if !sh.snap.SawPath(p) {
			slog.Debug("library: a file was tracked after the walk, walking the library again", "path", p)
			return false
		}
	}
	sh.lastID = maxID
	return true
}
