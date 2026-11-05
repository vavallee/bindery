package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate119KeysPendingReleasesPerBookAndKeepsRows: the rebuild that moves
// pending_releases from a guid key to a (guid, book) key (#3564) must carry
// every existing row across, keep the per book index 107 added, and then let a
// second book hold the same release.
func TestMigrate119KeysPendingReleasesPerBookAndKeepsRows(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := mkAuthor(t, authors, ctx, "OL-M119-A")
	newBook := func(fid string) *models.Book {
		t.Helper()
		b := &models.Book{
			ForeignID: fid, AuthorID: author.ID, Title: fid, SortTitle: fid,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := newBook("OL-M119-1")
	second := newBook("OL-M119-2")

	// The pre-119 table: guid unique on its own.
	for _, stmt := range []string{
		`DROP TABLE pending_releases`,
		`CREATE TABLE pending_releases (
		    id INTEGER PRIMARY KEY,
		    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
		    title TEXT NOT NULL, indexer_id INTEGER, guid TEXT NOT NULL UNIQUE,
		    protocol TEXT NOT NULL, size INTEGER, age_minutes INTEGER, quality TEXT,
		    custom_score INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL,
		    first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, release_json TEXT NOT NULL,
		    media_type TEXT NOT NULL DEFAULT 'ebook')`,
		`CREATE INDEX idx_pending_releases_book_id ON pending_releases(book_id)`,
	} {
		mustExec(t, database, stmt)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO pending_releases (id, book_id, title, indexer_id, guid, protocol, size, age_minutes,
		    quality, custom_score, reason, release_json, media_type)
		VALUES (41, ?, 'Held', 3, 'g-119', 'usenet', 2048, 12, 'epub', 5, 'usenet delay not met', '{"guid":"g-119"}', 'audiobook')`,
		first.ID); err != nil {
		t.Fatal(err)
	}

	v119 := migrationVersionForTest(t, "119_pending_releases_per_book.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v119); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 119: %v", err)
	}

	repo := NewPendingReleaseRepo(database)
	got, err := repo.GetByID(ctx, 41)
	if err != nil || got == nil {
		t.Fatalf("row lost by the rebuild: %+v, %v", got, err)
	}
	if got.BookID != first.ID || got.GUID != "g-119" || got.MediaType != "audiobook" || got.Quality != "epub" ||
		got.CustomScore != 5 || got.AgeMinutes != 12 || got.IndexerID == nil || *got.IndexerID != 3 {
		t.Errorf("row mangled by the rebuild: %+v", got)
	}

	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_pending_releases_book_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("idx_pending_releases_book_id is missing after the rebuild")
	}

	if err := repo.Upsert(ctx, &models.PendingRelease{
		BookID: second.ID, MediaType: "ebook", Title: "Held", GUID: "g-119", Protocol: "usenet",
		Reason: "usenet delay not met", ReleaseJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListByBook(ctx, second.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second book's rows = %+v, %v; want its own row", rows, err)
	}
}
