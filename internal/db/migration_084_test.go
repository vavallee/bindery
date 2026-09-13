package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate084BackfillsStatusForUnsatisfiedFormats covers #1634. A book that
// was imported as a single format and later widened to 'both' by Hardcover list
// sync or edition hydration kept status 'imported' while gaining a monitored
// format with no file. Every consumer of the wanted set selects on status
// alone, so the gap was never searched. Migration 084 repairs the rows that
// already exist; the widening sites now call ReevaluateStatus themselves.
func TestMigrate084BackfillsStatusForUnsatisfiedFormats(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	author := &models.Author{
		ForeignID: "OL:migration-084", Name: "Migration Author",
		SortName: "Author, Migration", MetadataProvider: "openlibrary",
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)

	// create seeds a book and forces status/media_type straight into the
	// columns, the shape a widened row is left in.
	create := func(foreignID, mediaType, status string) int64 {
		t.Helper()
		b := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID, Title: foreignID, SortTitle: foreignID,
			Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx,
			`UPDATE books SET status = ?, media_type = ? WHERE id = ?`, status, mediaType, b.ID); err != nil {
			t.Fatalf("seed %s: %v", foreignID, err)
		}
		return b.ID
	}

	// Widened after import: ebook on disk, audiobook slot empty, still 'imported'.
	widened := create("B-WIDENED", models.MediaTypeBoth, models.BookStatusImported)
	if err := books.AddBookFile(ctx, widened, models.MediaTypeEbook, "/library/widened.epub"); err != nil {
		t.Fatal(err)
	}
	// AddBookFile runs refreshBookStatus, which leaves an already-'imported' row
	// alone only when nothing is needed. Force the stuck value back afterwards.
	if _, err := database.ExecContext(ctx,
		`UPDATE books SET status = ? WHERE id = ?`, models.BookStatusImported, widened); err != nil {
		t.Fatal(err)
	}

	// Genuinely complete dual-format book: both files present, must stay imported.
	complete := create("B-COMPLETE", models.MediaTypeBoth, models.BookStatusImported)
	if err := books.AddBookFile(ctx, complete, models.MediaTypeEbook, "/library/complete.epub"); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, complete, models.MediaTypeAudiobook, "/library/complete.m4b"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx,
		`UPDATE books SET status = ? WHERE id = ?`, models.BookStatusImported, complete); err != nil {
		t.Fatal(err)
	}

	// Migration 028 shape: book_files row exists, scalar column never written.
	// The effective-path fallback must see the file and leave the row alone.
	legacy := create("B-LEGACY", models.MediaTypeEbook, models.BookStatusImported)
	if err := books.AddBookFile(ctx, legacy, models.MediaTypeEbook, "/library/legacy.epub"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx,
		`UPDATE books SET status = ?, ebook_file_path = '' WHERE id = ?`,
		models.BookStatusImported, legacy); err != nil {
		t.Fatal(err)
	}

	// Skipped encodes a user decision and must survive untouched.
	skipped := create("B-SKIPPED", models.MediaTypeBoth, models.BookStatusSkipped)

	v084 := migrationVersionForTest(t, "084_backfill_status_for_unsatisfied_formats.sql")
	rerun := func() {
		t.Helper()
		if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v084); err != nil {
			t.Fatalf("clear migration 084 marker: %v", err)
		}
		if err := migrate(database); err != nil {
			t.Fatalf("rerun migration 084: %v", err)
		}
	}
	rerun()

	rawStatus := func(id int64) string {
		t.Helper()
		var s string
		if err := database.QueryRowContext(ctx, `SELECT status FROM books WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatalf("read status %d: %v", id, err)
		}
		return s
	}

	if got := rawStatus(widened); got != models.BookStatusWanted {
		t.Errorf("widened row = %q, want wanted (this is the #1634 repair)", got)
	}
	if got := rawStatus(complete); got != models.BookStatusImported {
		t.Errorf("complete dual-format row = %q, want imported untouched", got)
	}
	if got := rawStatus(legacy); got != models.BookStatusImported {
		t.Errorf("migration-028-shaped row = %q, want imported: its file is in book_files", got)
	}
	if got := rawStatus(skipped); got != models.BookStatusSkipped {
		t.Errorf("skipped row = %q, want skipped untouched", got)
	}

	// Idempotent: a second run changes nothing.
	rerun()
	if got := rawStatus(complete); got != models.BookStatusImported {
		t.Errorf("second run changed the complete row to %q", got)
	}
	if got := rawStatus(widened); got != models.BookStatusWanted {
		t.Errorf("second run changed the repaired row to %q", got)
	}
}
