package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// fakeTracked is a book_files stand-in: rows get ids in insert order.
type fakeTracked struct {
	mu    sync.Mutex
	paths []string
	epoch uint64
	err   error
}

func (f *fakeTracked) track(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
}

func (f *fakeTracked) BookFilesFingerprint(context.Context) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, 0, f.err
	}
	return int64(len(f.paths)), int64(len(f.paths)), nil
}

func (f *fakeTracked) BookFilesPathEpoch() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.epoch
}

func (f *fakeTracked) BookFilePathsAfter(_ context.Context, afterID int64) ([]string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, afterID, f.err
	}
	if int(afterID) >= len(f.paths) {
		return nil, afterID, nil
	}
	return append([]string(nil), f.paths[afterID:]...), int64(len(f.paths)), nil
}

// shareInScope is what api.snapshotFinder does for each author sync of a
// batch: a fresh, unqueried snapshot offered to the scope.
func shareInScope(ctx context.Context, libDir string, tracked TrackedFiles) *LibrarySnapshot {
	return SnapshotScopeFrom(ctx).Share(ctx, NewLibrarySnapshot(libDir, ""), tracked)
}

func TestSnapshotScope_BatchWalksOnce(t *testing.T) {
	libDir := t.TempDir()
	const authors = 6
	for i := range authors {
		writeFile(t, filepath.Join(libDir, scopeAuthor(i), "Owned "+scopeAuthor(i)+".epub"))
	}
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	before := LibraryWalkCount(libDir)
	for i := range authors {
		snap := shareInScope(ctx, libDir, tracked)
		got := snap.FindExisting(ctx, "Owned "+scopeAuthor(i), scopeAuthor(i), models.MediaTypeEbook)
		if got == "" {
			t.Fatalf("author %d: owned file not found", i)
		}
		// The sync binds the file it found: a book_files row for a path
		// the walk already visited must not cost a walk.
		tracked.track(got)
	}
	if walks := LibraryWalkCount(libDir) - before; walks != 1 {
		t.Errorf("walks for %d author syncs in one scope = %d, want 1", authors, walks)
	}

	// Without a scope every sync builds its own snapshot: the old cost.
	before = LibraryWalkCount(libDir)
	for i := range authors {
		snap := shareInScope(context.Background(), libDir, tracked)
		snap.FindExisting(context.Background(), "Owned "+scopeAuthor(i), scopeAuthor(i), models.MediaTypeEbook)
	}
	if walks := LibraryWalkCount(libDir) - before; walks != authors {
		t.Errorf("walks for %d author syncs without a scope = %d, want %d", authors, walks, authors)
	}
}

func scopeAuthor(i int) string { return "Author " + string(rune('A'+i)) }

// TestSnapshotScope_ExternalFileMidBatchWaitsForNextBatch pins the stated
// window: a file another tool writes after the walk is not seen by later
// syncs of the same batch, and is seen by the next batch.
func TestSnapshotScope_ExternalFileMidBatchWaitsForNextBatch(t *testing.T) {
	libDir := t.TempDir()
	writeFile(t, filepath.Join(libDir, "Jane Doe", "First - Jane Doe.epub"))
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	shareInScope(ctx, libDir, tracked).FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook)

	late := filepath.Join(libDir, "Jane Doe", "Late - Jane Doe.epub")
	writeFile(t, late)

	if got := shareInScope(ctx, libDir, tracked).FindExisting(ctx, "Late", "Jane Doe", models.MediaTypeEbook); got != "" {
		t.Errorf("same batch saw an untracked file written after the walk: %q", got)
	}
	next := WithSnapshotScope(context.Background())
	if got := shareInScope(next, libDir, tracked).FindExisting(next, "Late", "Jane Doe", models.MediaTypeEbook); got != late {
		t.Errorf("next batch: got %q, want %q", got, late)
	}
}

// TestSnapshotScope_BinderyImportMidBatchIsSeen: a file Bindery imports and
// tracks after the walk is seen by the next sync of the batch, as it was
// when every sync built its own snapshot. It costs one more walk.
func TestSnapshotScope_BinderyImportMidBatchIsSeen(t *testing.T) {
	libDir := t.TempDir()
	writeFile(t, filepath.Join(libDir, "Jane Doe", "First - Jane Doe.epub"))
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	before := LibraryWalkCount(libDir)
	shareInScope(ctx, libDir, tracked).FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook)

	imported := filepath.Join(libDir, "Jane Doe", "Imported - Jane Doe.epub")
	writeFile(t, imported)
	tracked.track(imported)

	if got := shareInScope(ctx, libDir, tracked).FindExisting(ctx, "Imported", "Jane Doe", models.MediaTypeEbook); got != imported {
		t.Errorf("file tracked by Bindery mid batch: got %q, want %q", got, imported)
	}
	if walks := LibraryWalkCount(libDir) - before; walks != 2 {
		t.Errorf("walks = %d, want 2 (one at the start, one after the import)", walks)
	}
	// Reconciled: the next sync reuses the second walk.
	shareInScope(ctx, libDir, tracked).FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook)
	if walks := LibraryWalkCount(libDir) - before; walks != 2 {
		t.Errorf("walks after reconciling = %d, want still 2", walks)
	}
}

// TestSnapshotScope_ImportedFolderIsSeen: an audiobook import tracks its
// folder, not each track.
func TestSnapshotScope_ImportedFolderIsSeen(t *testing.T) {
	abDir := t.TempDir()
	writeFile(t, filepath.Join(abDir, "Jane Doe", "First", "First.m4b"))
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	share := func() *LibrarySnapshot {
		return SnapshotScopeFrom(ctx).Share(ctx, NewLibrarySnapshot("", abDir), tracked)
	}
	share().FindExisting(ctx, "First", "Jane Doe", models.MediaTypeAudiobook)
	// A folder the walk visited, tracked as a whole: no new walk.
	tracked.track(filepath.Join(abDir, "Jane Doe", "First"))
	before := LibraryWalkCount(abDir)
	share().FindExisting(ctx, "First", "Jane Doe", models.MediaTypeAudiobook)
	if walks := LibraryWalkCount(abDir) - before; walks != 0 {
		t.Errorf("tracking a visited folder cost %d walks, want 0", walks)
	}
	// A folder imported after the walk.
	folder := filepath.Join(abDir, "Jane Doe", "Second")
	writeFile(t, filepath.Join(folder, "Second.m4b"))
	tracked.track(folder)
	if got := share().FindExisting(ctx, "Second", "Jane Doe", models.MediaTypeAudiobook); got != filepath.Join(folder, "Second.m4b") {
		t.Errorf("imported folder: got %q", got)
	}
}

// TestSnapshotScope_PathRewriteWalksAgain: a reorganize moves tracked files
// in place, which the row ids cannot show.
func TestSnapshotScope_PathRewriteWalksAgain(t *testing.T) {
	libDir := t.TempDir()
	old := filepath.Join(libDir, "Jane Doe", "First - Jane Doe.epub")
	writeFile(t, old)
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	shareInScope(ctx, libDir, tracked).FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook)

	moved := filepath.Join(libDir, "Jane Doe", "First", "First - Jane Doe.epub")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	tracked.mu.Lock()
	tracked.epoch++
	tracked.mu.Unlock()

	if got := shareInScope(ctx, libDir, tracked).FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook); got != moved {
		t.Errorf("after a path rewrite: got %q, want %q", got, moved)
	}
}

// TestSnapshotScope_UnreadableTrackedFilesDoNotShare: a book_files error
// degrades to a snapshot per sync, never to a stale one.
func TestSnapshotScope_UnreadableTrackedFilesDoNotShare(t *testing.T) {
	libDir := t.TempDir()
	writeFile(t, filepath.Join(libDir, "Jane Doe", "First - Jane Doe.epub"))
	tracked := &fakeTracked{err: errors.New("database is locked")}
	ctx := WithSnapshotScope(context.Background())
	a := shareInScope(ctx, libDir, tracked)
	b := shareInScope(ctx, libDir, tracked)
	if a == b {
		t.Error("an unreadable book_files position still shared one snapshot")
	}
	// Readable at hand-out, unreadable at the next sync: walk again.
	tracked.err = nil
	c := shareInScope(ctx, libDir, tracked)
	c.FindExisting(ctx, "First", "Jane Doe", models.MediaTypeEbook)
	tracked.err = errors.New("database is locked")
	if d := shareInScope(ctx, libDir, tracked); d == c {
		t.Error("an unreadable book_files read kept the shared snapshot")
	}
}

// TestSnapshotScope_KeyedByLibraryConfiguration: a settings change mid batch
// gets its own snapshot.
func TestSnapshotScope_KeyedByLibraryConfiguration(t *testing.T) {
	libA, libB := t.TempDir(), t.TempDir()
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	scope := SnapshotScopeFrom(ctx)
	a := scope.Share(ctx, NewLibrarySnapshot(libA, ""), tracked)
	if again := scope.Share(ctx, NewLibrarySnapshot(libA, ""), tracked); again != a {
		t.Error("same configuration did not share")
	}
	if b := scope.Share(ctx, NewLibrarySnapshot(libB, ""), tracked); b == a {
		t.Error("another library root shared the first root's snapshot")
	}
	if c := scope.Share(ctx, NewLibrarySnapshot(libA, "").WithExcluded(filepath.Join(libA, "dl")), tracked); c == a {
		t.Error("other exclusions shared the snapshot")
	}
	if WithSnapshotScope(ctx) != ctx {
		t.Error("a nested batch did not reuse the outer scope")
	}
}

// TestLibrarySnapshot_RemovedFileIsNotOffered: the answer is checked on
// disk, and the next best file answers in its place.
func TestLibrarySnapshot_RemovedFileIsNotOffered(t *testing.T) {
	libDir := t.TempDir()
	epub := filepath.Join(libDir, "William Gibson", "Burning Chrome - William Gibson.epub")
	notes := filepath.Join(libDir, "William Gibson", "Burning Chrome (notes).txt")
	writeFile(t, epub)
	writeFile(t, notes)
	snap := NewLibrarySnapshot(libDir, "")
	ctx := context.Background()
	if got := snap.FindExisting(ctx, "Burning Chrome", "William Gibson", models.MediaTypeEbook); got != epub {
		t.Fatalf("got %q, want %q", got, epub)
	}
	if err := os.Remove(epub); err != nil {
		t.Fatal(err)
	}
	if got := snap.FindExisting(ctx, "Burning Chrome", "William Gibson", models.MediaTypeEbook); got != notes {
		t.Errorf("after removing the epub: got %q, want the remaining file %q", got, notes)
	}
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	if got := snap.FindExisting(ctx, "Burning Chrome", "William Gibson", models.MediaTypeEbook); got != "" {
		t.Errorf("after removing both: got %q, want nothing", got)
	}
}

// TestSnapshotScope_ConcurrentSyncs runs syncs of one batch in parallel, as
// refresh selected does. Run under -race.
func TestSnapshotScope_ConcurrentSyncs(t *testing.T) {
	libDir := t.TempDir()
	const authors = 8
	for i := range authors {
		writeFile(t, filepath.Join(libDir, scopeAuthor(i), "Owned "+scopeAuthor(i)+".epub"))
	}
	tracked := &fakeTracked{}
	ctx := WithSnapshotScope(context.Background())
	before := LibraryWalkCount(libDir)
	var wg sync.WaitGroup
	for i := range authors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap := shareInScope(ctx, libDir, tracked)
			got := snap.FindExisting(ctx, "Owned "+scopeAuthor(i), scopeAuthor(i), models.MediaTypeEbook)
			if got == "" {
				t.Errorf("author %d: not found", i)
				return
			}
			tracked.track(got)
		}()
	}
	wg.Wait()
	if walks := LibraryWalkCount(libDir) - before; walks != 1 {
		t.Errorf("walks = %d, want 1", walks)
	}
}
