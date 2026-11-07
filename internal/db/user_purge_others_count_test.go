package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestUserOwnedRows_CountsOtherAccountsBooksUnderOwnAuthors covers #3559 part 1.
// Purging removes the user's authors, and books.author_id is ON DELETE CASCADE,
// so books other accounts keep under those authors go too. The count has to
// show that before the admin picks purge, and it must not inflate Total(),
// which only gates whether a strategy is needed.
func TestUserOwnedRows_CountsOtherAccountsBooksUnderOwnAuthors(t *testing.T) {
	users, authors, database, adminID, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()

	// The fixture already has victim's own book under victim's author. Add one
	// book owned by the admin and one with no owner under the same author, each
	// with a file, plus an unrelated admin author with a book that must not count.
	seed := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (900, 'OL9W', ?, 'Theirs', 'Theirs', ?)", []any{authorID, adminID}},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (901, 'OL9X', ?, 'Shared', 'Shared', NULL)", []any{authorID}},
		{"INSERT INTO book_files (book_id, format, path) VALUES (900, 'ebook', '/lib/a.epub')", nil},
		{"INSERT INTO book_files (book_id, format, path) VALUES (900, 'audiobook', '/lib/a.m4b')", nil},
		{"INSERT INTO book_files (book_id, format, path) VALUES (901, 'ebook', '/lib/b.epub')", nil},
	}
	for _, s := range seed {
		if _, err := database.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.query, err)
		}
	}
	other := &models.Author{ForeignID: "OL2A", Name: "Admin Author", SortName: "Author, Admin"}
	if err := authors.CreateForUser(ctx, other, adminID); err != nil {
		t.Fatalf("create admin author: %v", err)
	}
	if _, err := database.Exec(
		"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (902, 'OL9Y', ?, 'Unrelated', 'Unrelated', ?)",
		other.ID, adminID,
	); err != nil {
		t.Fatalf("seed unrelated book: %v", err)
	}
	if _, err := database.Exec("INSERT INTO book_files (book_id, format, path) VALUES (902, 'ebook', '/lib/c.epub')"); err != nil {
		t.Fatalf("seed unrelated file: %v", err)
	}

	counts, err := users.OwnedRows(ctx, victimID)
	if err != nil {
		t.Fatalf("owned rows: %v", err)
	}
	if counts.OthersBooks != 2 {
		t.Errorf("OthersBooks = %d, want 2 (one owned by another account, one unowned)", counts.OthersBooks)
	}
	if counts.OthersBookFiles != 3 {
		t.Errorf("OthersBookFiles = %d, want 3", counts.OthersBookFiles)
	}
	if counts.Books != 1 {
		t.Errorf("Books = %d, want 1: own books are counted separately", counts.Books)
	}
	if counts.Total() != 4 {
		t.Errorf("Total = %d, want 4: the cascade counts must not change the gate", counts.Total())
	}

	adminCounts, err := users.OwnedRows(ctx, adminID)
	if err != nil {
		t.Fatalf("owned rows for admin: %v", err)
	}
	if adminCounts.OthersBooks != 0 || adminCounts.OthersBookFiles != 0 {
		t.Errorf("admin has no foreign books under own authors, got %+v", adminCounts)
	}
}
