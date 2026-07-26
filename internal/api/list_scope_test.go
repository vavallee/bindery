package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// listScopeFixture seeds two users — alice (the "other" user, id 1) and bob
// (the caller under test, id 2) — each owning one author + book, plus an
// unowned (owner_user_id NULL) "global" author + book. It returns the List
// handlers wired to the same DB so the admin-bypass / isolation matrix can be
// driven through the real HTTP handlers.
type listScopeFixture struct {
	database *sql.DB
	authorsH *AuthorHandler
	booksH   *BookHandler
	alice    int64
	bob      int64
}

func newListScopeFixture(t *testing.T) *listScopeFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	users := db.NewUserRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)

	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	aAlice := &models.Author{ForeignID: "ol-alice", Name: "Alice Author", SortName: "Author, Alice", Monitored: true}
	if err := authorRepo.CreateForUser(ctx, aAlice, alice.ID); err != nil {
		t.Fatalf("seed alice author: %v", err)
	}
	aBob := &models.Author{ForeignID: "ol-bob", Name: "Bob Author", SortName: "Author, Bob", Monitored: true}
	if err := authorRepo.CreateForUser(ctx, aBob, bob.ID); err != nil {
		t.Fatalf("seed bob author: %v", err)
	}
	aGlobal := &models.Author{ForeignID: "ol-global", Name: "Global Author", SortName: "Author, Global", Monitored: true}
	if err := authorRepo.Create(ctx, aGlobal); err != nil { // Create => NULL owner_user_id
		t.Fatalf("seed global author: %v", err)
	}

	seedBook := func(fid, title string, authorID, owner int64) {
		b := &models.Book{
			ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatalf("seed book %s: %v", title, err)
		}
		// books.Create does not set owner_user_id; set it explicitly (owner 0
		// leaves it NULL, modelling a pre-multiuser / imported book).
		if owner != 0 {
			if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", owner, b.ID); err != nil {
				t.Fatalf("set owner for %s: %v", title, err)
			}
		}
	}
	seedBook("b-alice", "Alice Book", aAlice.ID, alice.ID)
	seedBook("b-bob", "Bob Book", aBob.ID, bob.ID)
	seedBook("b-global", "Global Book", aGlobal.ID, 0)

	return &listScopeFixture{
		database: database,
		authorsH: NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, nil, nil),
		booksH:   NewBookHandler(bookRepo, nil, nil, nil),
		alice:    alice.ID,
		bob:      bob.ID,
	}
}

func (f *listScopeFixture) listAuthorNames(t *testing.T, ctx context.Context) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/authors?limit=500", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.authorsH.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authors list status %d: %s", rec.Code, rec.Body.String())
	}
	var resp authorListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode authors: %v", err)
	}
	names := make([]string, 0, len(resp.Items))
	for _, a := range resp.Items {
		names = append(names, a.Name)
	}
	return names
}

func (f *listScopeFixture) listBookTitles(t *testing.T, ctx context.Context) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/books?limit=500", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.booksH.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("books list status %d: %s", rec.Code, rec.Body.String())
	}
	var resp bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode books: %v", err)
	}
	titles := make([]string, 0, len(resp.Items))
	for _, b := range resp.Items {
		titles = append(titles, b.Title)
	}
	return titles
}

func assertSameSet(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("set mismatch:\n got  = %v\n want = %v", got, want)
	}
}

// TestListScope_Admin_SeesEverything is the bug reproduction: with tenancy on,
// an ADMIN (role "admin", id=2 = bob) listing authors/books must see the OTHER
// user's (alice, id=1) owned rows, the unowned/global rows, and their own.
// Pre-fix the handlers scoped strictly to UserIDFromContext with no admin
// bypass, so alice's author/book were invisible to the admin even though
// CheckOwnership lets the admin open them by ID — this test failed pre-fix.
func TestListScope_Admin_SeesEverything(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "admin")

	assertSameSet(t, f.listAuthorNames(t, ctx), []string{"Alice Author", "Bob Author", "Global Author"})
	assertSameSet(t, f.listBookTitles(t, ctx), []string{"Alice Book", "Bob Book", "Global Book"})
}

// TestListScope_NonAdmin_IsolatedPlusGlobal asserts a non-admin (role "user",
// id=2 = bob) sees ONLY their own rows plus the unowned/global rows, and NOT
// the other user's (alice) rows. The global-book visibility specifically
// exercises the books "OR owner_user_id IS NULL" fix.
func TestListScope_NonAdmin_IsolatedPlusGlobal(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "user")

	gotAuthors := f.listAuthorNames(t, ctx)
	assertSameSet(t, gotAuthors, []string{"Bob Author", "Global Author"})
	if slices.Contains(gotAuthors, "Alice Author") {
		t.Errorf("ISOLATION LEAK: non-admin bob saw alice's author: %v", gotAuthors)
	}

	gotBooks := f.listBookTitles(t, ctx)
	// "Global Book" present proves the OR-IS-NULL fix; "Alice Book" absent
	// proves isolation is preserved.
	assertSameSet(t, gotBooks, []string{"Bob Book", "Global Book"})
	if slices.Contains(gotBooks, "Alice Book") {
		t.Errorf("ISOLATION LEAK: non-admin bob saw alice's book: %v", gotBooks)
	}
}

// TestListScope_APIKey_Unscoped asserts that a request with no user identity
// (API-key / disabled / local-only — UserIDFromContext == 0) sees everything
// even with tenancy on, matching CheckOwnership's admin-equivalent handling.
func TestListScope_APIKey_Unscoped(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := context.Background() // no user id, no role

	assertSameSet(t, f.listAuthorNames(t, ctx), []string{"Alice Author", "Bob Author", "Global Author"})
	assertSameSet(t, f.listBookTitles(t, ctx), []string{"Alice Book", "Bob Book", "Global Book"})
}

// TestListScope_TenancyDisabled_Unscoped asserts that with the tenancy gate off
// (single-user default) even a non-admin context is unscoped — the per-user
// filter must not engage, preserving pre-multiuser behaviour.
func TestListScope_TenancyDisabled_Unscoped(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "user")

	assertSameSet(t, f.listAuthorNames(t, ctx), []string{"Alice Author", "Bob Author", "Global Author"})
	assertSameSet(t, f.listBookTitles(t, ctx), []string{"Alice Book", "Bob Book", "Global Book"})
}

func (f *listScopeFixture) listWantedTitles(t *testing.T, ctx context.Context) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.booksH.ListWanted(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wanted list status %d: %s", rec.Code, rec.Body.String())
	}
	var books []models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &books); err != nil {
		t.Fatalf("decode wanted: %v", err)
	}
	titles := make([]string, 0, len(books))
	for _, b := range books {
		titles = append(titles, b.Title)
	}
	return titles
}

// TestListScope_Wanted_NonAdminIsolated is the security regression: the
// /wanted/missing endpoint used the unscoped ListByStatus, so a non-admin saw
// every user's wanted books. It must now scope like the main book list —
// own + unowned only, never the other user's.
func TestListScope_Wanted_NonAdminIsolated(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "user")

	got := f.listWantedTitles(t, ctx)
	assertSameSet(t, got, []string{"Bob Book", "Global Book"})
	if slices.Contains(got, "Alice Book") {
		t.Errorf("ISOLATION LEAK: non-admin bob saw alice's wanted book: %v", got)
	}
}

// TestListScope_Wanted_AdminSeesEverything confirms the scoping does not hide
// other users' wanted books from an admin (scope userID 0 = unscoped).
func TestListScope_Wanted_AdminSeesEverything(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newListScopeFixture(t)
	ctx := auth.WithUserRole(auth.WithUserID(context.Background(), f.bob), "admin")

	assertSameSet(t, f.listWantedTitles(t, ctx), []string{"Alice Book", "Bob Book", "Global Book"})
}
