package calibre

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// newRollbackFixture wires an Importer that has run-tracking repos attached,
// so importing produces snapshots and rollback can be exercised end-to-end.
func newRollbackFixture(t *testing.T) (*Importer, *fakeReader, *db.AuthorRepo, *db.BookRepo, *db.EditionRepo, *db.CalibreImportRunRepo, *db.CalibreEntitySnapshotRepo, *db.CalibreProvenanceRepo) {
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

	return imp, fr, authorRepo, bookRepo, editionRepo, runsRepo, snapshotRepo, provRepo
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
