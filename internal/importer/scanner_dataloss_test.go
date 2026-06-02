package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// dataLossFixture builds an in-memory Scanner with a real author + wanted book
// and a completed download record, ready for tryImportInternal. importMode is
// written to settings before the import runs.
func dataLossFixture(t *testing.T, libraryDir, importMode string) (
	s *Scanner,
	dl *models.Download,
	dlRepo *db.DownloadRepo,
	bookRepo *db.BookRepo,
	ctx context.Context,
) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	bookRepo = db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)
	dlRepo = db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	s = NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")
	s.WithSettings(settingsRepo)
	if importMode != "" {
		if err := settingsRepo.Set(ctx, "import.mode", importMode); err != nil {
			t.Fatal(err)
		}
	}

	author := &models.Author{ForeignID: "OL-dl-test", Name: "Author A", SortName: "A, Author"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-dl-book",
		AuthorID:  author.ID,
		Title:     "Title T",
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeEbook,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	dl = &models.Download{
		GUID:   "guid-dl-test",
		Title:  book.Title,
		BookID: &book.ID,
		Status: models.StateCompleted,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return s, dl, dlRepo, bookRepo, ctx
}

// TestTryImportInternal_PartialFailureDoesNotDeleteUnlandedSource is the
// regression test for issue #705 finding 1.
//
// Scenario: a move-mode download contains two ebook files (epub + mobi). The
// epub imports cleanly; the mobi import is forced to fail (its destination path
// is pre-created as a directory, so the file write cannot succeed).
//
// Before the fix the terminal StateImported was written after the first
// successful file, and a later failure left a terminal-imported but incomplete
// download — and the move cleanup deleted the source of the file that never
// landed. After the fix the download must NOT be StateImported, and BOTH source
// files must still exist on disk so the import can be retried.
func TestTryImportInternal_PartialFailureDoesNotDeleteUnlandedSource(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	epubSrc := filepath.Join(downloadPath, "book.epub")
	mobiSrc := filepath.Join(downloadPath, "book.mobi")
	if err := os.WriteFile(epubSrc, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mobiSrc, []byte("mobi-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, dlRepo, _, ctx := dataLossFixture(t, libraryDir, "move")

	// Force the .mobi import to fail: pre-create its destination path AS A
	// DIRECTORY. os.Rename refuses to overwrite a directory with a file, and the
	// cross-filesystem fallback's os.Create fails the same way.
	mobiDest, err := s.renamer.DestPath(libraryDir, &models.Author{Name: "Author A"},
		&models.Book{Title: "Title T"}, "", "", mobiSrc)
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	if err := os.MkdirAll(mobiDest, 0o750); err != nil {
		t.Fatal(err)
	}

	s.tryImportInternal(ctx, dl, downloadPath, "transmission", "tor-1", nil, nil)

	// The download must NOT be terminal-imported: one file failed.
	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == models.StateImported {
		t.Errorf("download status = %q, want a failure state — a partial import must not be marked imported", got.Status)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("download status = %q, want %q (retryable partial-failure state)", got.Status, models.StateImportFailed)
	}

	// CRITICAL: the mobi source must still exist. The mobi file never landed in
	// the library, so its source is the only copy. Before the fix, the download
	// was marked StateImported after the epub and the move-mode RemoveAll then
	// destroyed the whole download folder — including this un-imported mobi.
	if _, err := os.Stat(mobiSrc); err != nil {
		t.Errorf("mobi source was deleted after a FAILED import — data loss: %v", err)
	}

	// The epub source is legitimately gone: move mode consumes each source as it
	// imports it (os.Rename). That is expected. What must NOT happen is the
	// download folder being RemoveAll'd as a unit — verify it still exists and
	// still holds the un-imported mobi.
	if _, err := os.Stat(downloadPath); err != nil {
		t.Errorf("download folder removed despite a partial failure — must be kept: %v", err)
	}

	// The original mobi content must be unchanged (not a truncated/partial copy).
	if data, err := os.ReadFile(mobiSrc); err != nil || string(data) != "mobi-bytes" {
		t.Errorf("mobi source corrupted/changed after failed import: data=%q err=%v", data, err)
	}
}

// TestCleanupMovedSources_NeverNukesSharedRoot is the regression test for issue
// #705 finding 4: move-mode cleanup must never delete a download path that is a
// shared save root holding other torrents' still-seeding data.
//
// Scenario: downloadPath is a save root that contains the imported torrent's
// file AND a sibling torrent's file. After importing (and deleting) our file,
// cleanup must leave the sibling's file — and therefore the directory — intact.
func TestCleanupMovedSources_NeverNukesSharedRoot(t *testing.T) {
	t.Parallel()

	s := &Scanner{libraryDir: t.TempDir()}
	sharedRoot := t.TempDir()

	ourFile := filepath.Join(sharedRoot, "our-book.epub")
	siblingFile := filepath.Join(sharedRoot, "another-torrent.bin")
	if err := os.WriteFile(ourFile, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingFile, []byte("sibling-seeding"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate: our file was already moved out (MoveFileCtx removes the source);
	// cleanup is then asked to prune. Remove ourFile to model the post-move state.
	if err := os.Remove(ourFile); err != nil {
		t.Fatal(err)
	}

	s.cleanupMovedSources(sharedRoot, []string{ourFile})

	// The sibling torrent's data and the shared root itself MUST survive.
	if _, err := os.Stat(siblingFile); err != nil {
		t.Errorf("sibling torrent file was destroyed by move cleanup — data loss: %v", err)
	}
	if _, err := os.Stat(sharedRoot); err != nil {
		t.Errorf("shared save root was removed even though it still holds sibling data: %v", err)
	}
}

// TestCleanupMovedSources_PrunesEmptyDownloadDir verifies the happy path: when
// the download path holds only the imported file (no siblings), cleanup deletes
// the file and prunes the now-empty directory tree.
func TestCleanupMovedSources_PrunesEmptyDownloadDir(t *testing.T) {
	t.Parallel()

	s := &Scanner{libraryDir: t.TempDir()}
	parent := t.TempDir()
	downloadPath := filepath.Join(parent, "MyTorrent")
	nested := filepath.Join(downloadPath, "sub")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	imported := filepath.Join(nested, "book.epub")
	if err := os.WriteFile(imported, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Model the post-move state: source already removed by MoveFileCtx.
	if err := os.Remove(imported); err != nil {
		t.Fatal(err)
	}

	s.cleanupMovedSources(downloadPath, []string{imported})

	if _, err := os.Stat(downloadPath); !os.IsNotExist(err) {
		t.Errorf("empty download dir should have been pruned, stat err = %v", err)
	}
	// The parent (above downloadPath) must NOT be touched.
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("parent above download path must never be pruned: %v", err)
	}
}

// TestCleanupMovedSources_RefusesLibraryRoot verifies the belt-and-braces
// guard: if downloadPath equals (or contains) a configured library root,
// cleanup must bail out entirely rather than risk deleting the library.
func TestCleanupMovedSources_RefusesLibraryRoot(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	s := &Scanner{libraryDir: libraryDir}

	libBook := filepath.Join(libraryDir, "existing.epub")
	if err := os.WriteFile(libBook, []byte("library-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// downloadPath == libraryDir: a misconfiguration that must be refused.
	s.cleanupMovedSources(libraryDir, []string{libBook})

	if _, err := os.Stat(libBook); err != nil {
		t.Errorf("library file destroyed — cleanup must refuse a download path equal to the library root: %v", err)
	}
	if _, err := os.Stat(libraryDir); err != nil {
		t.Errorf("library root destroyed by cleanup: %v", err)
	}
}

// TestMoveDirCtx_CancelDoesNotDeleteSource is the regression test for issue
// #705 finding 3: when the import context is cancelled mid-copy, MoveDirCtx
// must return promptly with ctx.Err() and must NOT delete the source directory.
//
// Before the fix the copy ran in a detached goroutine: MoveDirCtx returned
// ctx.Err() but the goroutine finished the copy and then os.RemoveAll(src),
// deleting the still-seeding source after the download was already marked
// failed.
func TestMoveDirCtx_CancelDoesNotDeleteSource(t *testing.T) {
	t.Parallel()

	// Build a source tree large enough that an already-cancelled context is
	// observed before the copy completes. The per-entry ctx check in
	// copyDirRooted guarantees a pre-cancelled context aborts immediately.
	src := t.TempDir()
	for i := 0; i < 50; i++ {
		name := filepath.Join(src, "sub", "part"+itoa(i)+".mp3")
		mustWrite(t, name, "audio-data-"+itoa(i))
	}
	// dst must be on a DIFFERENT filesystem branch only conceptually; here we
	// just need MoveDir's fast-path rename to be irrelevant. We force the slow
	// path by pre-creating dst's parent and pointing at a fresh dst — rename
	// would still succeed on the same fs, so instead we cancel BEFORE calling
	// and rely on moveDirCtx checking ctx. To exercise the copy path we cancel
	// the context up front: the rename fast-path may still fire, but if it does
	// the source is moved atomically (no detached goroutine, no data loss).
	dst := filepath.Join(t.TempDir(), "Author", "Title")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := MoveDirCtx(ctx, src, dst)

	// On the same filesystem os.Rename succeeds atomically even with a cancelled
	// context — that is safe (no partial state, no detached goroutine). The bug
	// only manifests on the cross-fs copy path. What MUST hold in every case:
	// the source data is never lost. Either it was atomically renamed to dst, or
	// it still sits at src — never deleted with the copy abandoned.
	srcExists := dirExists(src)
	dstExists := dirExists(dst)
	if !srcExists && !dstExists {
		t.Fatalf("data loss: source gone and destination absent after cancelled MoveDirCtx (err=%v)", err)
	}
	if srcExists {
		// Slow path was taken (or rename skipped): cancellation must have been
		// reported and the source left fully intact.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("MoveDirCtx err = %v, want context.Canceled when source is left in place", err)
		}
		for i := 0; i < 50; i++ {
			name := filepath.Join(src, "sub", "part"+itoa(i)+".mp3")
			if _, statErr := os.Stat(name); statErr != nil {
				t.Errorf("source file missing after cancelled copy — data loss: %v", statErr)
			}
		}
	}
}

// TestCopyDirContext_AbortsOnCancelledContext directly exercises the finding-3
// fix: copyDirContext (the shared recursive copy used by both CopyDir and the
// slow path of MoveDir) must abort with ctx.Err() instead of copying the whole
// tree. Before the fix the copy ignored ctx entirely and ran to completion in
// a detached goroutine spawned by MoveDirCtx/CopyDirCtx.
func TestCopyDirContext_AbortsOnCancelledContext(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	for i := 0; i < 40; i++ {
		mustWrite(t, filepath.Join(src, "sub", "part"+itoa(i)+".mp3"), "data-"+itoa(i))
	}
	dst := filepath.Join(t.TempDir(), "dest")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := copyDirContext(ctx, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copyDirContext err = %v, want context.Canceled", err)
	}
	// Source is never touched by copyDirContext.
	for i := 0; i < 40; i++ {
		if _, statErr := os.Stat(filepath.Join(src, "sub", "part"+itoa(i)+".mp3")); statErr != nil {
			t.Errorf("source file missing after cancelled copyDirContext: %v", statErr)
		}
	}
}

// TestMoveDirCtx_FailedCopyKeepsSource verifies finding 3's core safety
// guarantee at the MoveDir level: when the cross-filesystem copy fails,
// moveDirCtx must NOT os.RemoveAll the source. The copy is forced to fail by
// making the destination tree unwritable after the directory is created.
func TestMoveDirCtx_FailedCopyKeepsSource(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not block writes")
	}

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "part1.mp3"), "audio")
	mustWrite(t, filepath.Join(src, "part2.mp3"), "audio")

	// dstParent is read-only, so moveDirCtx's MkdirAll(filepath.Dir(dst)) — and
	// thus the whole move — fails before any rename/copy can succeed.
	dstParent := t.TempDir()
	if err := os.Chmod(dstParent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dstParent, 0o700) })
	dst := filepath.Join(dstParent, "locked", "Title")

	if err := MoveDirCtx(context.Background(), src, dst); err == nil {
		t.Fatal("expected MoveDirCtx to fail when the destination is unwritable")
	}

	// The source must be fully intact — a failed move must never delete it.
	for _, name := range []string{"part1.mp3", "part2.mp3"} {
		if _, err := os.Stat(filepath.Join(src, name)); err != nil {
			t.Errorf("source file %s deleted after a FAILED move — data loss: %v", name, err)
		}
	}
}

// TestCopyDirCtx_CancelLeavesSourceIntact verifies that a cancelled CopyDirCtx
// never removes the source (copy mode preserves seeding) and reports the
// cancellation rather than running the copy to completion in a detached
// goroutine.
func TestCopyDirCtx_CancelLeavesSourceIntact(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	for i := 0; i < 30; i++ {
		mustWrite(t, filepath.Join(src, "part"+itoa(i)+".mp3"), "audio-"+itoa(i))
	}
	dst := filepath.Join(t.TempDir(), "library", "Author", "Title")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CopyDirCtx(ctx, src, dst)

	// Source must always survive a copy, cancelled or not.
	for i := 0; i < 30; i++ {
		if _, statErr := os.Stat(filepath.Join(src, "part"+itoa(i)+".mp3")); statErr != nil {
			t.Errorf("source file removed by cancelled CopyDirCtx — copy mode must preserve the source: %v", statErr)
		}
	}
	if err == nil {
		t.Error("CopyDirCtx with a pre-cancelled context should return an error")
	}
}

// dirExists is a small test helper.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// itoa avoids pulling strconv into the test for a single use.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
