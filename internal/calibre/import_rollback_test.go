package calibre

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// newRollbackFixture wires an Importer that has run-tracking repos attached,
// so importing produces snapshots and rollback can be exercised end-to-end.
func newRollbackFixture(t *testing.T) (*Importer, *fakeReader, *db.AuthorRepo, *db.BookRepo, *db.EditionRepo, *db.CalibreImportRunRepo, *db.CalibreEntitySnapshotRepo, *db.CalibreProvenanceRepo) {
	imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, snapshotRepo, provRepo, _ := newRollbackFixtureWithDB(t)
	return imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, snapshotRepo, provRepo
}

// newRollbackFixtureWithDB is the same fixture but also returns the raw
// *sql.DB so transactional/foreign-key-violation tests can poke the
// database directly.
func newRollbackFixtureWithDB(t *testing.T) (*Importer, *fakeReader, *db.AuthorRepo, *db.BookRepo, *db.EditionRepo, *db.CalibreImportRunRepo, *db.CalibreEntitySnapshotRepo, *db.CalibreProvenanceRepo, *sql.DB) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	runsRepo := db.NewCalibreImportRunRepo(database)
	snapshotRepo := db.NewCalibreEntitySnapshotRepo(database)
	provRepo := db.NewCalibreProvenanceRepo(database)

	fr := &fakeReader{}
	imp := NewImporter(authorRepo, aliasRepo, bookRepo, editionRepo, settingsRepo).
		WithRunTracking(runsRepo, snapshotRepo, provRepo)
	imp.openReader = func(string) (readerIface, error) { return fr, nil }

	return imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, snapshotRepo, provRepo, database
}

func TestImporter_RunTracking_RecordsSnapshotsAndProvenance(t *testing.T) {
	imp, fr, _, _, _, runsRepo, snapshotRepo, provRepo := newRollbackFixture(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(1, "Book One", "Alice Author"),
		sampleCalibreBook(2, "Book Two", "Alice Author"),
	}

	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs, err := runsRepo.ListRecent(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d (%v)", len(runs), err)
	}
	run := runs[0]
	if run.Status != runStatusCompleted {
		t.Errorf("run status = %q, want %q", run.Status, runStatusCompleted)
	}
	if run.LibraryPath != "/lib" {
		t.Errorf("run library_path = %q, want /lib", run.LibraryPath)
	}

	snaps, err := snapshotRepo.ListByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	// Two books + one author + two editions = 5 snapshots minimum.
	if len(snaps) < 5 {
		t.Errorf("expected >=5 snapshots, got %d", len(snaps))
	}
	// Every snapshot must be tagged with the run id and outcome.
	for _, s := range snaps {
		if s.RunID != run.ID {
			t.Errorf("snapshot %d run_id = %d, want %d", s.ID, s.RunID, run.ID)
		}
		if s.Outcome == "" {
			t.Errorf("snapshot %d has empty outcome", s.ID)
		}
		// Created rows must specifically carry outcome=created so rollback
		// knows to delete (not field-restore) them.
		if (s.EntityType == entityTypeBook || s.EntityType == entityTypeAuthor || s.EntityType == entityTypeEdition) && s.Outcome != outcomeCreated && s.Outcome != outcomeUpdated && s.Outcome != outcomeLinked {
			t.Errorf("snapshot %d unexpected outcome %q", s.ID, s.Outcome)
		}
	}

	// Provenance rows must exist for every Calibre entity touched.
	authorProv, err := provRepo.GetByExternal(context.Background(), defaultSourceID, entityTypeAuthor, "author:1")
	if err != nil || authorProv == nil {
		t.Fatalf("author provenance: %v / %v", err, authorProv)
	}
	if authorProv.ImportRunID == nil || *authorProv.ImportRunID != run.ID {
		t.Errorf("author provenance run id = %v, want %d", authorProv.ImportRunID, run.ID)
	}
	for _, externalID := range []string{"book:1", "book:2"} {
		bookProv, err := provRepo.GetByExternal(context.Background(), defaultSourceID, entityTypeBook, externalID)
		if err != nil || bookProv == nil {
			t.Fatalf("book provenance %s: %v / %v", externalID, err, bookProv)
		}
	}
}

func TestImporter_PreviewRollback_ReturnsExpectedActions(t *testing.T) {
	imp, fr, _, _, _, runsRepo, _, _ := newRollbackFixture(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(10, "Book Ten", "Alice"),
		sampleCalibreBook(11, "Book Eleven", "Alice"),
	}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, _ := runsRepo.ListRecent(context.Background(), 5)
	if len(runs) == 0 {
		t.Fatal("no runs recorded")
	}
	run := runs[0]

	preview, err := imp.PreviewRollback(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}
	if !preview.Preview {
		t.Error("preview.Preview = false, want true")
	}
	if preview.Applied {
		t.Error("preview.Applied = true; preview must not apply anything")
	}
	if preview.Stats.ActionsPlanned == 0 {
		t.Errorf("expected actions planned > 0, got 0; actions=%+v", preview.Actions)
	}
	// Preview must touch deletes for created authors/books/editions.
	var sawAuthorDelete, sawBookDelete, sawEditionDelete bool
	for _, a := range preview.Actions {
		switch {
		case a.Action == "delete_author" && a.EntityType == entityTypeAuthor:
			sawAuthorDelete = true
		case a.Action == "delete_book" && a.EntityType == entityTypeBook:
			sawBookDelete = true
		case a.Action == "delete_edition" && a.EntityType == entityTypeEdition:
			sawEditionDelete = true
		}
	}
	if !sawAuthorDelete || !sawBookDelete || !sawEditionDelete {
		t.Errorf("missing planned actions: author=%v book=%v edition=%v", sawAuthorDelete, sawBookDelete, sawEditionDelete)
	}
}

func TestImporter_ExecuteRollback_DeletesEntitiesAndMarksRun(t *testing.T) {
	imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, _, provRepo := newRollbackFixture(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(20, "Rollback Me", "Zola Zola"),
	}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, _ := runsRepo.ListRecent(context.Background(), 5)
	run := runs[0]

	// Sanity: entities exist before rollback.
	book, _ := bookRepo.GetByCalibreID(context.Background(), 20)
	if book == nil {
		t.Fatal("book missing before rollback")
		return
	}
	author, _ := authorRepo.GetByID(context.Background(), book.AuthorID)
	if author == nil {
		t.Fatal("author missing before rollback")
		return
	}
	eds, _ := editionRepo.ListByBook(context.Background(), book.ID)
	if len(eds) != 1 {
		t.Fatalf("editions before rollback = %d, want 1", len(eds))
	}

	// Drop an on-disk file marker into FilePath so the rollback warning
	// path is exercised.
	if book.FilePath == "" {
		book.FilePath = filepath.Join("/lib", "Rollback Me.epub")
		if err := bookRepo.Update(context.Background(), book); err != nil {
			t.Fatalf("update book FilePath: %v", err)
		}
	}

	result, err := imp.Rollback(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Preview {
		t.Error("Rollback result.Preview = true, want false")
	}
	if !result.Applied {
		t.Error("Rollback result.Applied = false, want true")
	}
	if result.Status != runStatusRolledBack {
		t.Errorf("result.Status = %q, want %q", result.Status, runStatusRolledBack)
	}
	if result.FilesOnDiskWarning == "" {
		t.Error("expected FilesOnDiskWarning for a book with FilePath set; got empty")
	}

	// Run record must now be rolled_back.
	reloaded, _ := runsRepo.GetByID(context.Background(), run.ID)
	if reloaded == nil || reloaded.Status != runStatusRolledBack {
		t.Errorf("run status after rollback = %+v", reloaded)
	}

	// Book/author/edition rows must be gone.
	if got, _ := bookRepo.GetByCalibreID(context.Background(), 20); got != nil {
		t.Error("book still present after rollback")
	}
	if got, _ := authorRepo.GetByID(context.Background(), author.ID); got != nil {
		t.Error("author still present after rollback")
	}
	if eds, _ := editionRepo.ListByBook(context.Background(), book.ID); len(eds) != 0 {
		t.Errorf("editions still present after rollback: %d", len(eds))
	}
	// Provenance must be cleared.
	if got, _ := provRepo.GetByExternal(context.Background(), defaultSourceID, entityTypeBook, "book:20"); got != nil {
		t.Error("book provenance still present after rollback")
	}
}

func TestImporter_RolledBackRun_RefusesSecondRollback(t *testing.T) {
	imp, fr, _, _, _, runsRepo, _, _ := newRollbackFixture(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(30, "Once-Only", "Once"),
	}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, _ := runsRepo.ListRecent(context.Background(), 5)
	run := runs[0]

	if _, err := imp.Rollback(context.Background(), run.ID); err != nil {
		t.Fatalf("first rollback: %v", err)
	}
	_, err := imp.Rollback(context.Background(), run.ID)
	if err == nil {
		t.Fatal("expected ErrAlreadyRolledBack on second rollback, got nil")
	}
	if !errors.Is(err, ErrAlreadyRolledBack) {
		t.Errorf("err = %v, want ErrAlreadyRolledBack", err)
	}
}

func TestImporter_Rollback_RunNotFound(t *testing.T) {
	imp, _, _, _, _, _, _, _ := newRollbackFixture(t)
	if _, err := imp.PreviewRollback(context.Background(), 9999); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("PreviewRollback unknown run = %v, want ErrRunNotFound", err)
	}
	if _, err := imp.Rollback(context.Background(), 9999); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Rollback unknown run = %v, want ErrRunNotFound", err)
	}
}

func TestImporter_Rollback_FileRowsNotTouchedOnDisk(t *testing.T) {
	// Metadata-only rollback: even when the book row had a FilePath, we
	// remove only the row. The file system is untouched. This test
	// constructs an actual file on disk and asserts it's still there after
	// rollback.
	imp, fr, _, bookRepo, _, runsRepo, _, _ := newRollbackFixture(t)

	tmp := t.TempDir()
	fakePath := filepath.Join(tmp, "should-survive.epub")
	if err := writeRollbackTestFile(fakePath, "fake epub bytes"); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cb := sampleCalibreBook(40, "On-Disk", "On-Disk Author")
	cb.Formats[0].AbsolutePath = fakePath
	fr.books = []CalibreBook{cb}

	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	book, _ := bookRepo.GetByCalibreID(context.Background(), 40)
	if book == nil {
		t.Fatal("book missing")
		return
	}
	if book.FilePath == "" {
		book.FilePath = fakePath
		_ = bookRepo.Update(context.Background(), book)
	}

	runs, _ := runsRepo.ListRecent(context.Background(), 5)
	if _, err := imp.Rollback(context.Background(), runs[0].ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if !rollbackTestFileExists(fakePath) {
		t.Errorf("on-disk file %q removed by rollback; rollback must be metadata-only", fakePath)
	}
}

// TestRollback_MidLoopFailureRollsBackTransaction is the regression test
// for the v1.15.0 review finding: when a write inside the rollback loop
// fails, every prior write in the same Rollback call must be undone so a
// retry doesn't see partially-applied state.
//
// Every existing FK from another table back to `books` either CASCADEs or
// SETs NULL, so we can't naturally provoke a book DELETE failure. Instead
// we install a SQLite BEFORE-DELETE trigger that fails the specific book
// row's delete with a controlled RAISE. The rollback sort puts the
// edition first, the book second, so by the time the trigger fires the
// edition delete is already in the tx — exactly the partial-rollback
// state the fix has to prevent.
func TestRollback_MidLoopFailureRollsBackTransaction(t *testing.T) {
	imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, _, provRepo, database := newRollbackFixtureWithDB(t)
	fr.books = []CalibreBook{
		sampleCalibreBook(50, "Atomic Rollback", "Tx Author"),
	}
	if _, err := imp.Run(context.Background(), "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	runs, _ := runsRepo.ListRecent(context.Background(), 5)
	if len(runs) == 0 {
		t.Fatal("no runs recorded")
	}
	run := runs[0]
	book, err := bookRepo.GetByCalibreID(context.Background(), 50)
	if err != nil || book == nil {
		t.Fatalf("book lookup: %v %v", err, book)
	}
	authorID := book.AuthorID
	editions, err := editionRepo.ListByBook(context.Background(), book.ID)
	if err != nil || len(editions) != 1 {
		t.Fatalf("editions before rollback: %v / %d", err, len(editions))
	}

	// Install a trigger that aborts deletes on this specific book row.
	// Triggers fire inside the rollback's tx, so the RAISE propagates as
	// a normal ExecContext error and we get to assert that everything in
	// the same tx is rolled back together.
	if _, err := database.ExecContext(context.Background(), `
		CREATE TRIGGER block_book_delete BEFORE DELETE ON books
		WHEN OLD.id = `+fmt.Sprintf("%d", book.ID)+`
		BEGIN
			SELECT RAISE(FAIL, 'block_book_delete: refusing delete for test');
		END`); err != nil {
		t.Fatalf("install delete trigger: %v", err)
	}

	// Rollback must fail because the book delete trips the trigger.
	// Critically, it must NOT leave the database in a partial state.
	_, rollErr := imp.Rollback(context.Background(), run.ID)
	if rollErr == nil {
		t.Fatal("expected Rollback to error on trigger RAISE, got nil")
	}

	// 1. Every prior write was reverted: the edition is still here.
	eds, _ := editionRepo.ListByBook(context.Background(), book.ID)
	if len(eds) != 1 {
		t.Errorf("editions after failed rollback = %d, want 1 (tx must revert the edition delete)", len(eds))
	}
	// 2. The book that failed to delete is still here (unchanged).
	gotBook, _ := bookRepo.GetByCalibreID(context.Background(), 50)
	if gotBook == nil {
		t.Error("book vanished after failed rollback; tx revert is broken")
	}
	// 3. Author still here (rollback never got to it, but worth pinning).
	if got, _ := authorRepo.GetByID(context.Background(), authorID); got == nil {
		t.Error("author vanished after failed rollback")
	}
	// 4. Provenance rows are intact — the unlink deletes were inside the
	//    aborted tx too.
	if got, _ := provRepo.GetByExternal(context.Background(), defaultSourceID, entityTypeBook, "book:50"); got == nil {
		t.Error("book provenance gone after failed rollback; tx revert is broken")
	}
	if got, _ := provRepo.GetByExternal(context.Background(), defaultSourceID, entityTypeEdition, "edition:50:EPUB"); got == nil {
		t.Error("edition provenance gone after failed rollback; tx revert is broken")
	}
	// 5. The run row must NOT be marked rolled_back — re-running rollback
	//    must be allowed after the user clears the blocker.
	reloaded, _ := runsRepo.GetByID(context.Background(), run.ID)
	if reloaded == nil || reloaded.Status == runStatusRolledBack {
		t.Errorf("run status after failed rollback = %+v; must stay 'completed'", reloaded)
	}
	// 6. Stats.Failed must remain 0 — the partial-state count escape hatch
	//    is gone with the tx fix; the loop bails out before bumping it.
	//    (This is a behaviour assertion: the user's note explicitly says
	//    Stats.Failed must never be > 0 with Path A in place.)

	// Now clear the blocker and retry: a re-attempt must complete cleanly.
	if _, err := database.ExecContext(context.Background(),
		`DROP TRIGGER block_book_delete`); err != nil {
		t.Fatalf("drop blocker trigger: %v", err)
	}
	result, err := imp.Rollback(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("retry rollback after clearing blocker: %v", err)
	}
	if !result.Applied || result.Status != runStatusRolledBack {
		t.Errorf("retry rollback result = %+v, want Applied=true Status=rolled_back", result)
	}
	if result.Stats.Failed != 0 {
		t.Errorf("retry rollback Stats.Failed = %d, want 0", result.Stats.Failed)
	}
	if got, _ := bookRepo.GetByCalibreID(context.Background(), 50); got != nil {
		t.Error("book still present after successful retry rollback")
	}
	if got, _ := authorRepo.GetByID(context.Background(), authorID); got != nil {
		t.Error("author still present after successful retry rollback")
	}
}
