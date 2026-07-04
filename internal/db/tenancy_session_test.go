package db_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// tenancyFixture seeds two users, each owning one author + one book, and
// returns the repos and the two user IDs. It is the shared setup for the
// cross-tenant isolation tests below.
type tenancyFixture struct {
	database *sql.DB
	users    *db.UserRepo
	authors  *db.AuthorRepo
	books    *db.BookRepo
	userA    int64
	userB    int64
	authorA  int64
	authorB  int64
	bookA    int64
	bookB    int64
}

func newTenancyFixture(t *testing.T) *tenancyFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	users := db.NewUserRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	uA, err := users.Create(ctx, "alice", "hashA")
	if err != nil {
		t.Fatalf("create user alice: %v", err)
	}
	uB, err := users.Create(ctx, "bob", "hashB")
	if err != nil {
		t.Fatalf("create user bob: %v", err)
	}

	aA := &models.Author{ForeignID: "ol-author-A", Name: "Alice Author", SortName: "Author, Alice", Monitored: true}
	if err := authors.CreateForUser(ctx, aA, uA.ID); err != nil {
		t.Fatalf("create author for alice: %v", err)
	}
	aB := &models.Author{ForeignID: "ol-author-B", Name: "Bob Author", SortName: "Author, Bob", Monitored: true}
	if err := authors.CreateForUser(ctx, aB, uB.ID); err != nil {
		t.Fatalf("create author for bob: %v", err)
	}

	// Books: Create() does not set owner_user_id, so set it explicitly the same
	// way the existing multiuser test does. Use a wanted+monitored status so the
	// books are visible to ListByStatusAndUser("wanted").
	bA := &models.Book{
		ForeignID: "book-A", AuthorID: aA.ID, Title: "Alice Book", SortTitle: "Alice Book",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, bA); err != nil {
		t.Fatalf("create book for alice: %v", err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uA.ID, bA.ID); err != nil {
		t.Fatalf("set book A owner: %v", err)
	}

	bB := &models.Book{
		ForeignID: "book-B", AuthorID: aB.ID, Title: "Bob Book", SortTitle: "Bob Book",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, bB); err != nil {
		t.Fatalf("create book for bob: %v", err)
	}
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", uB.ID, bB.ID); err != nil {
		t.Fatalf("set book B owner: %v", err)
	}

	return &tenancyFixture{
		database: database, users: users, authors: authors, books: books,
		userA: uA.ID, userB: uB.ID,
		authorA: aA.ID, authorB: aB.ID,
		bookA: bA.ID, bookB: bB.ID,
	}
}

// TestBookRepo_ListByAuthorAndUser_TenantIsolation asserts that the author-scoped
// list only returns books owned by the requesting user, and crucially that
// querying user A against user B's author returns NOTHING — an IDOR guard: a
// guessed/leaked authorID must not leak another tenant's books.
func TestBookRepo_ListByAuthorAndUser_TenantIsolation(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	// Positive: A sees A's own book under A's author.
	got, err := f.books.ListByAuthorAndUser(ctx, f.authorA, f.userA)
	if err != nil {
		t.Fatalf("ListByAuthorAndUser(authorA, userA): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Alice Book" {
		t.Fatalf("userA/authorA = %v, want [Alice Book]", titles(got))
	}

	// Negative (the security assertion): A querying B's author returns nothing.
	// B's book has owner_user_id=userB, so the owner_user_id=userA predicate
	// must filter it out even though authorB is supplied.
	leak, err := f.books.ListByAuthorAndUser(ctx, f.authorB, f.userA)
	if err != nil {
		t.Fatalf("ListByAuthorAndUser(authorB, userA): %v", err)
	}
	if len(leak) != 0 {
		t.Errorf("CROSS-TENANT LEAK: userA querying authorB returned %v, want []", titles(leak))
	}

	// Symmetric: B sees only B's book.
	gotB, err := f.books.ListByAuthorAndUser(ctx, f.authorB, f.userB)
	if err != nil {
		t.Fatalf("ListByAuthorAndUser(authorB, userB): %v", err)
	}
	if len(gotB) != 1 || gotB[0].Title != "Bob Book" {
		t.Errorf("userB/authorB = %v, want [Bob Book]", titles(gotB))
	}
}

// TestBookRepo_ListByStatusAndUser_TenantIsolation asserts the status-scoped list
// returns only the requesting user's books and never another tenant's, even
// though both books share the same "wanted" status.
func TestBookRepo_ListByStatusAndUser_TenantIsolation(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	gotA, err := f.books.ListByStatusAndUser(ctx, models.BookStatusWanted, f.userA)
	if err != nil {
		t.Fatalf("ListByStatusAndUser(userA): %v", err)
	}
	if len(gotA) != 1 || gotA[0].Title != "Alice Book" {
		t.Fatalf("userA wanted = %v, want exactly [Alice Book]", titles(gotA))
	}
	// Negative: B's book (same status) must be absent from A's results.
	for _, b := range gotA {
		if b.Title == "Bob Book" {
			t.Errorf("CROSS-TENANT LEAK: userA's wanted list contains Bob Book")
		}
	}

	gotB, err := f.books.ListByStatusAndUser(ctx, models.BookStatusWanted, f.userB)
	if err != nil {
		t.Fatalf("ListByStatusAndUser(userB): %v", err)
	}
	if len(gotB) != 1 || gotB[0].Title != "Bob Book" {
		t.Errorf("userB wanted = %v, want exactly [Bob Book]", titles(gotB))
	}
}

// TestBookRepo_GetByForeignIDForUser_TenantIsolation asserts a user cannot
// resolve another tenant's book by its foreign id (issue #1210). The book
// scope uses strict owner_user_id equality.
func TestBookRepo_GetByForeignIDForUser_TenantIsolation(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	// Positive: A resolves A's own foreign id.
	own, err := f.books.GetByForeignIDForUser(ctx, "book-A", f.userA)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(book-A, userA): %v", err)
	}
	if own == nil || own.Title != "Alice Book" {
		t.Fatalf("userA/book-A = %v, want Alice Book", own)
	}

	// Negative: A must NOT resolve B's foreign id.
	leak, err := f.books.GetByForeignIDForUser(ctx, "book-B", f.userA)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(book-B, userA): %v", err)
	}
	if leak != nil {
		t.Errorf("CROSS-TENANT LEAK: userA resolved book-B (owned by B) = %q", leak.Title)
	}

	// Symmetric negative for B.
	leakB, err := f.books.GetByForeignIDForUser(ctx, "book-A", f.userB)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(book-A, userB): %v", err)
	}
	if leakB != nil {
		t.Errorf("CROSS-TENANT LEAK: userB resolved book-A (owned by A) = %q", leakB.Title)
	}
}

// TestAuthorRepo_GetByForeignIDForUser_TenantIsolation asserts a user cannot
// resolve another tenant's author by foreign id. The author scope is
// owner_user_id = userID OR owner_user_id IS NULL, so a NULL-owned (global)
// author IS visible to everyone — we assert both the negative (B's owned
// author hidden from A) and that global authors remain visible.
func TestAuthorRepo_GetByForeignIDForUser_TenantIsolation(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	// Positive: A resolves A's own author.
	own, err := f.authors.GetByForeignIDForUser(ctx, "ol-author-A", f.userA)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(ol-author-A, userA): %v", err)
	}
	if own == nil || own.Name != "Alice Author" {
		t.Fatalf("userA author = %v, want Alice Author", own)
	}

	// Negative: A must NOT resolve B's owned author.
	leak, err := f.authors.GetByForeignIDForUser(ctx, "ol-author-B", f.userA)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(ol-author-B, userA): %v", err)
	}
	if leak != nil {
		t.Errorf("CROSS-TENANT LEAK: userA resolved ol-author-B (owned by B) = %q", leak.Name)
	}

	// Global (NULL-owner) author is visible to any user per the query's
	// "OR owner_user_id IS NULL" clause. Create one (no owner) and confirm A sees it.
	global := &models.Author{ForeignID: "ol-author-global", Name: "Global Author", SortName: "Author, Global"}
	if err := f.authors.Create(ctx, global); err != nil {
		t.Fatalf("create global author: %v", err)
	}
	g, err := f.authors.GetByForeignIDForUser(ctx, "ol-author-global", f.userA)
	if err != nil {
		t.Fatalf("GetByForeignIDForUser(global, userA): %v", err)
	}
	if g == nil || g.Name != "Global Author" {
		t.Errorf("userA should see NULL-owner global author; got %v", g)
	}
}

// TestAuthorRepo_GetByAnyForeignIDForUser_TenantIsolation asserts the
// identifier-aware lookup also enforces tenancy: A resolving B's author by a
// secondary identifier must return nothing.
func TestAuthorRepo_GetByAnyForeignIDForUser_TenantIsolation(t *testing.T) {
	f := newTenancyFixture(t)
	ctx := context.Background()

	// Attach a secondary identifier to each author so the JOIN branch of
	// GetByAnyForeignID(ForUser) is exercised.
	if _, err := f.database.Exec(
		"INSERT INTO author_identifiers (author_id, provider, foreign_id) VALUES (?, ?, ?)",
		f.authorA, "hardcover", "alt-A"); err != nil {
		t.Fatalf("insert identifier alt-A: %v", err)
	}
	if _, err := f.database.Exec(
		"INSERT INTO author_identifiers (author_id, provider, foreign_id) VALUES (?, ?, ?)",
		f.authorB, "hardcover", "alt-B"); err != nil {
		t.Fatalf("insert identifier alt-B: %v", err)
	}

	// Positive: A resolves A's author via the secondary identifier.
	own, err := f.authors.GetByAnyForeignIDForUser(ctx, "alt-A", f.userA)
	if err != nil {
		t.Fatalf("GetByAnyForeignIDForUser(alt-A, userA): %v", err)
	}
	if own == nil || own.Name != "Alice Author" {
		t.Fatalf("userA alt-A = %v, want Alice Author", own)
	}

	// Negative (security): A must NOT resolve B's author via B's secondary id.
	leak, err := f.authors.GetByAnyForeignIDForUser(ctx, "alt-B", f.userA)
	if err != nil {
		t.Fatalf("GetByAnyForeignIDForUser(alt-B, userA): %v", err)
	}
	if leak != nil {
		t.Errorf("CROSS-TENANT LEAK: userA resolved alt-B (owned by B) = %q", leak.Name)
	}

	// Negative also via B's primary foreign id (the GetByForeignIDForUser branch).
	leakPrimary, err := f.authors.GetByAnyForeignIDForUser(ctx, "ol-author-B", f.userA)
	if err != nil {
		t.Fatalf("GetByAnyForeignIDForUser(ol-author-B, userA): %v", err)
	}
	if leakPrimary != nil {
		t.Errorf("CROSS-TENANT LEAK: userA resolved ol-author-B via Any = %q", leakPrimary.Name)
	}
}

// TestUserRepo_SessionEpoch exercises the session-revocation primitive: a fresh
// user starts at epoch 0, BumpSessionEpoch increments it, and the new value is
// persisted (re-read from the DB, not just returned in-memory).
func TestUserRepo_SessionEpoch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)

	u, err := users.Create(ctx, "carol", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Initial epoch is 1: migration 047 defaults session_epoch to 1 (not 0) so
	// that pre-migration cookies, which decode as epoch=0, fail the comparison
	// on upgrade. Create() returns the same value it just wrote.
	const initialEpoch = 1
	if u.SessionEpoch != initialEpoch {
		t.Errorf("Create returned SessionEpoch=%d, want %d", u.SessionEpoch, initialEpoch)
	}
	got, err := users.GetSessionEpoch(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetSessionEpoch initial: %v", err)
	}
	if got != initialEpoch {
		t.Fatalf("initial epoch = %d, want %d", got, initialEpoch)
	}

	// Bump increments by one and is persisted.
	if err := users.BumpSessionEpoch(ctx, u.ID); err != nil {
		t.Fatalf("BumpSessionEpoch: %v", err)
	}
	got, err = users.GetSessionEpoch(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetSessionEpoch after bump: %v", err)
	}
	if got != initialEpoch+1 {
		t.Fatalf("epoch after one bump = %d, want %d", got, initialEpoch+1)
	}

	// A second bump confirms monotonic increment, re-read from a fresh repo so
	// the value can only come from the DB (no in-memory caching).
	if err := users.BumpSessionEpoch(ctx, u.ID); err != nil {
		t.Fatalf("second BumpSessionEpoch: %v", err)
	}
	freshRepo := db.NewUserRepo(database)
	got, err = freshRepo.GetSessionEpoch(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetSessionEpoch persisted: %v", err)
	}
	if got != initialEpoch+2 {
		t.Errorf("persisted epoch after two bumps = %d, want %d", got, initialEpoch+2)
	}

	// Unknown user yields (0, nil) per the documented contract.
	missing, err := users.GetSessionEpoch(ctx, 999999)
	if err != nil {
		t.Fatalf("GetSessionEpoch(missing): %v", err)
	}
	if missing != 0 {
		t.Errorf("missing-user epoch = %d, want 0", missing)
	}
}

func titles(books []models.Book) []string {
	out := make([]string, len(books))
	for i, b := range books {
		out[i] = b.Title
	}
	return out
}
