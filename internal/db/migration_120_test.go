package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate120ScopesExistingBlocklistRows: existing entries keep blocking
// what they blocked when the download client's verdict made them (reason
// prefixes 'downloadFailed: ' and 'stalled: ') or they have no book; every
// other entry becomes a judgement about its own book (#3563).
func TestMigrate120ScopesExistingBlocklistRows(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-M120-A")
	b := &models.Book{
		ForeignID: "OL-M120-1", AuthorID: author.ID, Title: "t", SortTitle: "t",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	mustExec(t, database, `ALTER TABLE blocklist DROP COLUMN scope`)
	rows := []struct {
		guid   string
		bookID any
		reason string
		want   string
	}{
		{"g-failed", b.ID, "downloadFailed: FAILURE/PAR", models.BlocklistScopeRelease},
		{"g-stalled", b.ID, "stalled: no peers / no download progress", models.BlocklistScopeRelease},
		{"g-readarr", nil, "", models.BlocklistScopeRelease},
		{"g-format", b.ID, "format mobi is not allowed. Not imported.", models.BlocklistScopeBook},
		{"g-history", b.ID, "downloadFailed", models.BlocklistScopeBook},
	}
	for _, r := range rows {
		if _, err := database.ExecContext(ctx, `INSERT INTO blocklist (book_id, guid, title, reason) VALUES (?, ?, 't', ?)`,
			r.bookID, r.guid, r.reason); err != nil {
			t.Fatal(err)
		}
	}

	v120 := migrationVersionForTest(t, "120_blocklist_scope.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v120); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 120: %v", err)
	}

	entries, err := NewBlocklistRepo(database).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.GUID] = e.Scope
	}
	for _, r := range rows {
		if got[r.guid] != r.want {
			t.Errorf("%s: scope = %q, want %q", r.guid, got[r.guid], r.want)
		}
	}
}

// TestBlocklistRepo_ScopeAndBlocksBook covers the write side of #3563: the
// scope a new entry is stored with, and BlocksBook, the per book question the
// History action asks before adding an entry.
func TestBlocklistRepo_ScopeAndBlocksBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-BLS-A")
	mk := func(fid string) *models.Book {
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
	a, b := mk("OL-BLS-1"), mk("OL-BLS-2")
	repo := NewBlocklistRepo(database)

	unscoped := &models.BlocklistEntry{BookID: &a.ID, GUID: "g-book", Title: "t"}
	if err := repo.Create(ctx, unscoped); err != nil {
		t.Fatal(err)
	}
	if unscoped.Scope != models.BlocklistScopeBook {
		t.Errorf("an entry with a book and no scope = %q, want book", unscoped.Scope)
	}
	noBook := &models.BlocklistEntry{GUID: "g-nobook", Title: "t", Scope: models.BlocklistScopeBook}
	if err := repo.Create(ctx, noBook); err != nil {
		t.Fatal(err)
	}
	if noBook.Scope != models.BlocklistScopeRelease {
		t.Errorf("an entry with no book = %q, want release, since a book scope with no book blocks nothing", noBook.Scope)
	}
	if err := repo.Create(ctx, &models.BlocklistEntry{BookID: &a.ID, GUID: "g-release", Title: "t", Scope: models.BlocklistScopeRelease}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		guid   string
		bookID *int64
		want   bool
	}{
		{"g-book", &a.ID, true},
		{"g-book", &b.ID, false},
		{"g-book", nil, false},
		{"g-release", &b.ID, true},
		{"g-release", nil, true},
		{"g-nobook", &b.ID, true},
		{"g-missing", &a.ID, false},
	} {
		got, err := repo.BlocksBook(ctx, tc.guid, tc.bookID)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("BlocksBook(%s, %v) = %v, want %v", tc.guid, tc.bookID, got, tc.want)
		}
	}

	// A book scoped entry whose book is deleted blocks nothing.
	if _, err := database.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.GUID == "g-book" && (e.Scope != models.BlocklistScopeBook || e.BlocksBook(b.ID)) {
			t.Errorf("orphaned book entry %+v must keep its scope and block no other book", e)
		}
	}
}
