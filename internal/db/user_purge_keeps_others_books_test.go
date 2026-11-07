package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestUserDelete_PurgeKeepsOtherAccountsBooks covers #3559. books.author_id is
// ON DELETE CASCADE, so deleting the purged user's authors used to take every
// book under them, including ones another account owns. A purge now removes
// only the departing user's own rows: an author that still holds someone
// else's books stays, handed to the single account that owns them, or made
// shared when the remaining books have several owners or one has none.
func TestUserDelete_PurgeKeepsOtherAccountsBooks(t *testing.T) {
	users, authors, database, adminID, victimID, authorID := newDeleteFixture(t)
	ctx := context.Background()

	third, err := users.Create(ctx, "third", "pw")
	if err != nil {
		t.Fatalf("create third user: %v", err)
	}

	// authorID (fixture): victim's own book plus one admin book -> goes to admin.
	// mixed: books owned by admin and third -> shared.
	// unowned: a shared book -> shared.
	// lonely: only victim's own book -> deleted with the victim.
	mixed := &models.Author{ForeignID: "OL3A", Name: "Mixed", SortName: "Mixed"}
	unowned := &models.Author{ForeignID: "OL4A", Name: "Unowned", SortName: "Unowned"}
	lonely := &models.Author{ForeignID: "OL5A", Name: "Lonely", SortName: "Lonely"}
	for _, a := range []*models.Author{mixed, unowned, lonely} {
		if err := authors.CreateForUser(ctx, a, victimID); err != nil {
			t.Fatalf("create author %s: %v", a.Name, err)
		}
	}
	seed := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (900, 'B900', ?, 'Admin Book', 'Admin Book', ?)", []any{authorID, adminID}},
		{"INSERT INTO book_files (book_id, format, path) VALUES (900, 'ebook', '/lib/a.epub')", nil},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (901, 'B901', ?, 'Admin Mixed', 'Admin Mixed', ?)", []any{mixed.ID, adminID}},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (902, 'B902', ?, 'Third Mixed', 'Third Mixed', ?)", []any{mixed.ID, third.ID}},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (903, 'B903', ?, 'Shared', 'Shared', NULL)", []any{unowned.ID}},
		{"INSERT INTO books (id, foreign_id, author_id, title, sort_title, owner_user_id) VALUES (904, 'B904', ?, 'Victim Only', 'Victim Only', ?)", []any{lonely.ID, victimID}},
	}
	for _, s := range seed {
		if _, err := database.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.query, err)
		}
	}

	if err := users.Delete(ctx, victimID, UserDeletePlan{Strategy: PurgeOwnedRows}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	for _, id := range []int64{900, 901, 902, 903} {
		var n int
		if err := database.QueryRow("SELECT COUNT(*) FROM books WHERE id = ?", id).Scan(&n); err != nil || n != 1 {
			t.Errorf("book %d survived = %d (err %v), want 1", id, n, err)
		}
	}
	var files int
	if err := database.QueryRow("SELECT COUNT(*) FROM book_files WHERE book_id = 900").Scan(&files); err != nil || files != 1 {
		t.Errorf("book 900 files = %d (err %v), want 1", files, err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM books WHERE id = 904").Scan(&files); err != nil || files != 0 {
		t.Errorf("the purged user's own book survived: %d (err %v)", files, err)
	}

	owner := func(id int64) (found bool, owner *int64) {
		var o *int64
		err := database.QueryRow("SELECT owner_user_id FROM authors WHERE id = ?", id).Scan(&o)
		return err == nil, o
	}
	if ok, o := owner(authorID); !ok || o == nil || *o != adminID {
		t.Errorf("author with one other owner: found=%v owner=%v, want admin %d", ok, o, adminID)
	}
	if ok, o := owner(mixed.ID); !ok || o != nil {
		t.Errorf("author with several owners: found=%v owner=%v, want shared (NULL)", ok, o)
	}
	if ok, o := owner(unowned.ID); !ok || o != nil {
		t.Errorf("author with a shared book: found=%v owner=%v, want shared (NULL)", ok, o)
	}
	if ok, _ := owner(lonely.ID); ok {
		t.Error("author holding only the purged user's books should be deleted")
	}
}
