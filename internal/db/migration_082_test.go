package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate082RewritesRetiredDownloadStatuses covers the #2374 cleanup.
// 'downloading' and 'downloaded' were valid book statuses that no Bindery code
// path ever wrote; the only way one reached the column was a script driving
// PUT /api/v1/book/{id} by hand. Both values are gone now, so a row still
// holding one would render an unknown status pill and appear on no list.
// Migration 082 rewrites them to 'wanted' and leaves every real status, and
// the monitored flag, alone.
func TestMigrate082RewritesRetiredDownloadStatuses(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authors := NewAuthorRepo(database)
	author := &models.Author{
		ForeignID:        "OL:migration-082",
		Name:             "Migration Author",
		SortName:         "Author, Migration",
		MetadataProvider: "openlibrary",
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	books := NewBookRepo(database)
	create := func(foreignID string) int64 {
		t.Helper()
		b := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID, Title: foreignID, SortTitle: foreignID,
			Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	// The two retired values can no longer be expressed as constants, so write
	// them the way a third-party client did: straight into the column.
	downloading := create("B-DOWNLOADING")
	downloaded := create("B-DOWNLOADED")
	imported := create("B-IMPORTED")
	skipped := create("B-SKIPPED")
	for id, status := range map[int64]string{
		downloading: "downloading",
		downloaded:  "downloaded",
		imported:    models.BookStatusImported,
		skipped:     models.BookStatusSkipped,
	} {
		if _, err := database.ExecContext(ctx, `UPDATE books SET status = ? WHERE id = ?`, status, id); err != nil {
			t.Fatalf("seed status %q: %v", status, err)
		}
	}

	v082 := migrationVersionForTest(t, "082_drop_dead_book_statuses.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v082); err != nil {
		t.Fatalf("clear migration 082 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 082: %v", err)
	}

	statusOf := func(id int64) string {
		t.Helper()
		b, err := books.GetByID(ctx, id)
		if err != nil || b == nil {
			t.Fatalf("reload book %d: book=%+v err=%v", id, b, err)
		}
		if !b.Monitored {
			t.Errorf("book %d lost its monitored flag; the migration must not touch it", id)
		}
		return b.Status
	}
	if got := statusOf(downloading); got != models.BookStatusWanted {
		t.Errorf("'downloading' row = %q, want wanted", got)
	}
	if got := statusOf(downloaded); got != models.BookStatusWanted {
		t.Errorf("'downloaded' row = %q, want wanted", got)
	}
	if got := statusOf(imported); got != models.BookStatusImported {
		t.Errorf("imported row = %q, want imported untouched", got)
	}
	if got := statusOf(skipped); got != models.BookStatusSkipped {
		t.Errorf("skipped row = %q, want skipped untouched", got)
	}

	// Idempotent: a second run matches nothing and changes nothing.
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v082); err != nil {
		t.Fatalf("clear migration 082 marker again: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 082 a second time: %v", err)
	}
	if got := statusOf(imported); got != models.BookStatusImported {
		t.Errorf("second run changed the imported row to %q", got)
	}
}
