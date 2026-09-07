package calibre

import (
	"context"
	"path/filepath"
	"testing"
)

// TestImporter_BookFileRecordsProvenance is the remaining half of #1635. #2195
// made the import register a book_files row per Calibre format but recorded no
// provenance for them, so the rows sat outside the rollback machinery
// migration 044 exists to provide.
func TestImporter_BookFileRecordsProvenance(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, snapshotRepo, provRepo := newSeriesRollbackFixture(t)
	ctx := context.Background()
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}

	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	book, err := bookRepo.GetByCalibreID(ctx, 1)
	if err != nil || book == nil {
		t.Fatalf("book not found post-import: %v", err)
	}
	path := filepath.Join("/lib", "Book One.epub")
	externalID := calibreBookFileExternalID(path)

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("book_files = %+v, want one row at %s", files, path)
	}

	prov, err := provRepo.GetByExternal(ctx, defaultSourceID, entityTypeBookFile, externalID)
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if prov == nil {
		t.Fatal("no calibre_provenance row for the book file (CHECK constraint rejected it?)")
	}
	if prov.LocalID != book.ID {
		t.Errorf("provenance local_id = %d, want book id %d", prov.LocalID, book.ID)
	}
	runID := latestRunID(t, runsRepo)
	if prov.ImportRunID == nil || *prov.ImportRunID != runID {
		t.Errorf("provenance import_run_id = %v, want %d", prov.ImportRunID, runID)
	}

	snaps, err := snapshotRepo.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	snap := findSnapshot(snaps, entityTypeBookFile)
	if snap == nil {
		t.Fatal("no calibre_entity_snapshots row for the book file, so rollback would never see it")
	}
	if snap.Outcome != outcomeCreated {
		t.Errorf("book file snapshot outcome = %q, want %q", snap.Outcome, outcomeCreated)
	}
	if snap.ExternalID != externalID {
		t.Errorf("book file snapshot external_id = %q, want %q", snap.ExternalID, externalID)
	}
}

// TestRollback_UntracksBookFileOnAPreexistingBook is the case the FK cascade
// does not already cover. For a book the run CREATED, deleting the book takes
// its book_files rows with it (028 is ON DELETE CASCADE). For a book that
// already existed, rollback restores the book and the file row it gained
// during the run would survive without an explicit untrack.
func TestRollback_UntracksBookFileOnAPreexistingBook(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, _, provRepo := newSeriesRollbackFixture(t)
	ctx := context.Background()

	// First run creates the book. Roll nothing back; this is the "already
	// existed" setup for the second run.
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	book, err := bookRepo.GetByCalibreID(ctx, 1)
	if err != nil || book == nil {
		t.Fatalf("book not found: %v", err)
	}

	// The file then moves inside the Calibre library. The second run registers
	// the new path beside the old one, and only the new row is this run's to
	// unwind.
	moved := filepath.Join("/lib", "Relocated", "Book One.epub")
	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.Formats = []CalibreFormat{{Format: "EPUB", FileName: "book", AbsolutePath: moved}}
	fr.books = []CalibreBook{cb}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	runID := latestRunID(t, runsRepo)

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("book_files = %+v, want 2 rows (original plus relocated)", files)
	}

	preview, err := imp.PreviewRollback(ctx, runID)
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}
	if !hasAction(preview.Actions, entityTypeBookFile, "untrack_file") {
		t.Fatalf("preview has no untrack_file action: %+v", preview.Actions)
	}

	if _, err := imp.Rollback(ctx, runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	after, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles after rollback: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("book_files after rollback = %+v, want only the row the first run created", after)
	}
	if after[0].Path == moved {
		t.Errorf("rollback kept the row it created (%s) and dropped the earlier one", moved)
	}
	prov, err := provRepo.GetByExternal(ctx, defaultSourceID, entityTypeBookFile, calibreBookFileExternalID(moved))
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	if prov != nil {
		t.Errorf("book file provenance survived rollback: %+v", prov)
	}
}

// A path a real download placed is never this run's to unwind, even though the
// import refreshes its provenance. Mirrors the ownership narrowing #1868
// applied to series links.
func TestRollback_LeavesAFileItDidNotInsert(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, _, _ := newSeriesRollbackFixture(t)
	ctx := context.Background()

	// The first run creates the book and inserts the file row.
	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	book, err := bookRepo.GetByCalibreID(ctx, 1)
	if err != nil || book == nil {
		t.Fatalf("book not found: %v", err)
	}
	path := filepath.Join("/lib", "Book One.epub")

	// A second run re-reports the same path. AddIfMissing does not insert, so
	// this run must not claim it.
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	runID := latestRunID(t, runsRepo)

	preview, err := imp.PreviewRollback(ctx, runID)
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}
	if hasAction(preview.Actions, entityTypeBookFile, "untrack_file") {
		t.Fatalf("second run planned to untrack a file it did not insert: %+v", preview.Actions)
	}

	if _, err := imp.Rollback(ctx, runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	after, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	found := false
	for _, f := range after {
		if f.Path == path {
			found = true
		}
	}
	if !found {
		t.Errorf("rollback removed a file row an earlier run created: %+v", after)
	}
}

// Untracking a file row is exactly what the on-disk warning is for: Bindery
// drops its record of a file that is still on the filesystem. A pre-existing
// book keeps its own row, so without counting the untrack the caller would be
// told nothing at all.
func TestRollback_UntrackWarnsAboutFilesLeftOnDisk(t *testing.T) {
	imp, fr, _, bookRepo, runsRepo, _, _ := newSeriesRollbackFixture(t)
	ctx := context.Background()

	fr.books = []CalibreBook{sampleCalibreBook(1, "Book One", "Alice Author")}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	book, err := bookRepo.GetByCalibreID(ctx, 1)
	if err != nil || book == nil {
		t.Fatalf("book not found: %v", err)
	}

	cb := sampleCalibreBook(1, "Book One", "Alice Author")
	cb.Formats = []CalibreFormat{{Format: "EPUB", FileName: "book", AbsolutePath: "/lib/Relocated/Book One.epub"}}
	fr.books = []CalibreBook{cb}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("second run: %v", err)
	}

	result, err := imp.Rollback(ctx, latestRunID(t, runsRepo))
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.FilesOnDiskWarning == "" {
		t.Error("no FilesOnDiskWarning after untracking a file that is still on disk")
	}
	if result.Stats.FilesAffected != 1 {
		t.Errorf("FilesAffected = %d, want 1 (the book counted once, not per action)", result.Stats.FilesAffected)
	}
}
