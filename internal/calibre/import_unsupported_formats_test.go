package calibre

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// polishedCalibreBook is a book Calibre's Polish / Fix EPUB has touched: the
// working EPUB, the untouched backup Calibre keeps as ORIGINAL_EPUB, and an
// audiobook format beside them (#3630).
func polishedCalibreBook(id int64, title, author string) CalibreBook {
	cb := sampleCalibreBook(id, title, author)
	base := filepath.Join("/lib", title)
	cb.Formats = []CalibreFormat{
		{Format: "EPUB", FileName: title, AbsolutePath: base + ".epub"},
		{Format: "ORIGINAL_EPUB", FileName: title, AbsolutePath: base + ".original_epub"},
		{Format: "M4B", FileName: title, AbsolutePath: base + ".m4b"},
	}
	return cb
}

func trackedPaths(t *testing.T, books *db.BookRepo, bookID int64) []string {
	t.Helper()
	files, err := books.ListFiles(context.Background(), bookID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

// TestImporter_SkipsFormatsBinderyCannotUse is #3630: every format in
// metadata.db used to become a tracked book file, so each Polish backup showed
// up as an ebook Bindery can neither open, remap nor deliver.
func TestImporter_SkipsFormatsBinderyCannotUse(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newSeriesRollbackFixture(t)
	ctx := context.Background()
	fr.books = []CalibreBook{polishedCalibreBook(1, "Book One", "Alice Author")}

	stats, err := imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	book, err := bookRepo.GetByCalibreID(ctx, 1)
	if err != nil || book == nil {
		t.Fatalf("book not found: %v", err)
	}
	got := trackedPaths(t, bookRepo, book.ID)
	want := []string{"/lib/Book One.epub", "/lib/Book One.m4b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("tracked = %v, want %v", got, want)
	}
	if stats.FormatsSkipped != 1 {
		t.Errorf("FormatsSkipped = %d, want 1", stats.FormatsSkipped)
	}
}

// A Kobo kepub keeps passing: its last extension is .epub.
func TestImporter_KeepsKepubEpub(t *testing.T) {
	imp, fr, _, bookRepo, _, _, _ := newSeriesRollbackFixture(t)
	ctx := context.Background()
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.Formats = []CalibreFormat{{Format: "EPUB", FileName: "Book One.kepub", AbsolutePath: "/lib/Book One.kepub.epub"}}
	fr.books = []CalibreBook{cb}

	stats, err := imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	book, _ := bookRepo.GetByCalibreID(ctx, 1)
	if got := trackedPaths(t, bookRepo, book.ID); len(got) != 1 || got[0] != "/lib/Book One.kepub.epub" {
		t.Fatalf("tracked = %v, want the kepub", got)
	}
	if stats.FormatsSkipped != 0 {
		t.Errorf("FormatsSkipped = %d, want 0", stats.FormatsSkipped)
	}
}

// plantCalibreCreatedFile reproduces a row an import from before #3630 left
// behind: a book_files row, its provenance, and the run's "created" snapshot,
// exactly what registerBookFiles wrote for every format.
func plantCalibreCreatedFile(t *testing.T, books *db.BookRepo, snaps *db.CalibreEntitySnapshotRepo, prov *db.CalibreProvenanceRepo, runID, bookID int64, path string) {
	t.Helper()
	ctx := context.Background()
	if _, err := books.AddBookFileIfMissing(ctx, bookID, models.MediaTypeEbook, path); err != nil {
		t.Fatalf("AddBookFileIfMissing: %v", err)
	}
	externalID := calibreBookFileExternalID(path)
	rid := runID
	if err := prov.Upsert(ctx, &models.CalibreProvenance{SourceID: defaultSourceID, EntityType: entityTypeBookFile, ExternalID: externalID, LocalID: bookID, ImportRunID: &rid}); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if err := snaps.Record(ctx, &models.CalibreEntitySnapshot{RunID: runID, SourceID: defaultSourceID, EntityType: entityTypeBookFile, ExternalID: externalID, LocalID: bookID, Outcome: outcomeCreated}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
}

// TestImporter_RemovesUnsupportedFilesEarlierImportsCreated is the one time
// cleanup for libraries that already carry the rows (the reporter had 503).
// Only rows the Calibre import's own records say it created go; a row with
// the same extension that anything else added stays, and nothing on disk is
// touched.
func TestImporter_RemovesUnsupportedFilesEarlierImportsCreated(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, snapRepo, provRepo := newSeriesRollbackFixture(t)
	ctx := context.Background()
	dir := t.TempDir()

	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	epub := filepath.Join(dir, "Book One.epub")
	cb.Formats = []CalibreFormat{{Format: "EPUB", FileName: "Book One", AbsolutePath: epub}}
	other := sampleCalibreBook(2, "Book Two", "Alice Author")
	other.Formats = []CalibreFormat{{Format: "EPUB", FileName: "Book Two", AbsolutePath: filepath.Join(dir, "Book Two.epub")}}
	fr.books = []CalibreBook{cb, other}
	if _, err := imp.Run(ctx, dir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRun := latestRunID(t, runsRepo)
	book, _ := bookRepo.GetByCalibreID(ctx, 1)
	book2, _ := bookRepo.GetByCalibreID(ctx, 2)

	// What an older import left: a backup it tracked, on disk beside the book.
	backup := filepath.Join(dir, "Book One.original_epub")
	for _, p := range []string{epub, backup} {
		if err := os.WriteFile(p, []byte("book"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plantCalibreCreatedFile(t, bookRepo, snapRepo, provRepo, firstRun, book.ID, backup)

	// Same extension, added by something other than the Calibre import: no
	// provenance at all.
	userAdded := filepath.Join(dir, "Book Two.original_epub")
	if err := bookRepo.AddBookFile(ctx, book2.ID, models.MediaTypeEbook, userAdded); err != nil {
		t.Fatalf("AddBookFile: %v", err)
	}
	// Same extension, placed by something else and only refreshed by an
	// import: provenance but no "created" snapshot. Not the import's row.
	refreshed := filepath.Join(dir, "Book Two.original_azw3")
	if err := bookRepo.AddBookFile(ctx, book2.ID, models.MediaTypeEbook, refreshed); err != nil {
		t.Fatalf("AddBookFile: %v", err)
	}
	rid := firstRun
	if err := provRepo.Upsert(ctx, &models.CalibreProvenance{SourceID: defaultSourceID, EntityType: entityTypeBookFile, ExternalID: calibreBookFileExternalID(refreshed), LocalID: book2.ID, ImportRunID: &rid}); err != nil {
		t.Fatal(err)
	}

	stats, err := imp.Run(ctx, dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats.UnsupportedFilesRemoved != 1 {
		t.Errorf("UnsupportedFilesRemoved = %d, want 1", stats.UnsupportedFilesRemoved)
	}
	if got := trackedPaths(t, bookRepo, book.ID); len(got) != 1 || got[0] != epub {
		t.Errorf("book one tracked = %v, want only %s", got, epub)
	}
	got2 := trackedPaths(t, bookRepo, book2.ID)
	want2 := []string{filepath.Join(dir, "Book Two.epub"), userAdded, refreshed}
	sort.Strings(want2)
	if len(got2) != 3 || got2[0] != want2[0] || got2[1] != want2[1] || got2[2] != want2[2] {
		t.Errorf("book two tracked = %v, want %v", got2, want2)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("cleanup touched the file on disk: %v", err)
	}
	if p, _ := provRepo.GetByExternal(ctx, defaultSourceID, entityTypeBookFile, calibreBookFileExternalID(backup)); p != nil {
		t.Errorf("provenance for the removed row survived: %+v", p)
	}

	// A further run has nothing left to remove.
	stats, err = imp.Run(ctx, dir)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if stats.UnsupportedFilesRemoved != 0 {
		t.Errorf("third run UnsupportedFilesRemoved = %d, want 0", stats.UnsupportedFilesRemoved)
	}

	// Rolling back the run that created the removed row still works.
	if _, err := imp.Rollback(ctx, firstRun); err != nil {
		t.Fatalf("Rollback of the creating run: %v", err)
	}
}

// A book whose only tracked file was the backup ends up exactly where removing
// that file entry by hand leaves a book: no ebook path and the same status.
func TestImporter_CleanupLeavesAFilelessBookLikeAManualRemove(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, snapRepo, provRepo := newSeriesRollbackFixture(t)
	ctx := context.Background()

	backupOnly := func(id int64, title string) CalibreBook {
		cb := sampleCalibreBook(id, title, "Alice Author")
		cb.Formats = []CalibreFormat{{Format: "ORIGINAL_EPUB", FileName: title, AbsolutePath: filepath.Join("/lib", title+".original_epub")}}
		return cb
	}
	fr.books = []CalibreBook{backupOnly(1, "Book One"), backupOnly(2, "Book Two")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	cleaned, _ := bookRepo.GetByCalibreID(ctx, 1)
	manual, _ := bookRepo.GetByCalibreID(ctx, 2)

	plantCalibreCreatedFile(t, bookRepo, snapRepo, provRepo, latestRunID(t, runsRepo), cleaned.ID, "/lib/Book One.original_epub")
	if err := bookRepo.AddBookFile(ctx, manual.ID, models.MediaTypeEbook, "/lib/Book Two.original_epub"); err != nil {
		t.Fatal(err)
	}
	if b, _ := bookRepo.GetByID(ctx, cleaned.ID); b.Status != models.BookStatusImported {
		t.Fatalf("setup: book with a tracked file has status %q", b.Status)
	}
	if _, err := bookRepo.RemoveBookFile(ctx, "/lib/Book Two.original_epub"); err != nil {
		t.Fatal(err)
	}

	stats, err := imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats.UnsupportedFilesRemoved != 1 {
		t.Fatalf("UnsupportedFilesRemoved = %d, want 1", stats.UnsupportedFilesRemoved)
	}
	a, _ := bookRepo.GetByID(ctx, cleaned.ID)
	b, _ := bookRepo.GetByID(ctx, manual.ID)
	if a.EbookFilePath != "" {
		t.Errorf("cleaned book still renders %q", a.EbookFilePath)
	}
	if a.Status != b.Status {
		t.Errorf("cleaned book status %q, manual remove gives %q", a.Status, b.Status)
	}
}

// Rollback after a run that skipped formats: the skipped format left nothing
// to unwind, and the formats the run did track come off cleanly, on a book the
// run created and on one that already existed.
func TestRollback_AfterARunWithSkippedFormats(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, _, _ := newSeriesRollbackFixture(t)
	ctx := context.Background()

	// Run one: a book with only an EPUB, kept as the "already existed" book.
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	existing, _ := bookRepo.GetByCalibreID(ctx, 1)

	// Run two: that book gains a backup and an audiobook, and a new polished
	// book arrives.
	grown := sampleCalibreBook(1, "Book One", "Alice Author")
	grown.Formats = append(grown.Formats,
		CalibreFormat{Format: "ORIGINAL_EPUB", FileName: "Book One", AbsolutePath: "/lib/Book One.original_epub"},
		CalibreFormat{Format: "M4B", FileName: "Book One", AbsolutePath: "/lib/Book One.m4b"},
	)
	fr.books = []CalibreBook{grown, polishedCalibreBook(2, "Book Two", "Alice Author")}
	stats, err := imp.Run(ctx, "/lib")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats.FormatsSkipped != 2 {
		t.Fatalf("FormatsSkipped = %d, want 2", stats.FormatsSkipped)
	}
	runID := latestRunID(t, runsRepo)

	result, err := imp.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.Failed != 0 {
		t.Errorf("rollback failures = %d, want 0", result.Stats.Failed)
	}
	if got := trackedPaths(t, bookRepo, existing.ID); len(got) != 1 || got[0] != "/lib/Book One.epub" {
		t.Errorf("existing book tracked after rollback = %v, want only the first run's EPUB", got)
	}
	if b, _ := bookRepo.GetByCalibreID(ctx, 2); b != nil {
		t.Errorf("book the rolled back run created survived: %+v", b)
	}
}
