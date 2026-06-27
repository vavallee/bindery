package db_test

import (
	"context"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestBookRepo_ListPageFiltered_IncludesNullOwner pins the books-list predicate
// fix: a scoped list (UserID = a real user) must include the user's own books
// AND unowned (owner_user_id NULL) books — mirroring the authors list and
// CheckOwnership, which treat a NULL owner as visible to everyone — while still
// excluding another user's owned books. A UserID of 0 must be fully unscoped.
func TestBookRepo_ListPageFiltered_IncludesNullOwner(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	// Add a third, unowned (NULL owner) book alongside the fixture's owned ones.
	globalBook := &models.Book{
		ForeignID: "book-global", AuthorID: f.authorA, Title: "Global Book", SortTitle: "Global Book",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := f.books.Create(ctx, globalBook); err != nil { // Create leaves owner_user_id NULL
		t.Fatalf("create global book: %v", err)
	}

	titlesFor := func(userID int64) []string {
		books, _, err := f.books.ListPageFiltered(ctx, db.BookListFilter{UserID: userID}, 500, 0)
		if err != nil {
			t.Fatalf("ListPageFiltered(userID=%d): %v", userID, err)
		}
		return titles(books)
	}

	// Scoped to user A: own book + the NULL-owner book, but NOT user B's book.
	gotA := titlesFor(f.userA)
	slices.Sort(gotA)
	wantA := []string{"Alice Book", "Global Book"}
	if !slices.Equal(gotA, wantA) {
		t.Errorf("userA scoped list = %v, want %v (own + NULL-owner, no cross-tenant)", gotA, wantA)
	}
	if slices.Contains(gotA, "Bob Book") {
		t.Errorf("CROSS-TENANT LEAK: userA saw Bob Book: %v", gotA)
	}

	// userID 0 = unscoped: every book regardless of owner.
	gotAll := titlesFor(0)
	slices.Sort(gotAll)
	wantAll := []string{"Alice Book", "Bob Book", "Global Book"}
	if !slices.Equal(gotAll, wantAll) {
		t.Errorf("unscoped (userID=0) list = %v, want %v", gotAll, wantAll)
	}
}
