package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// Authors are keyed per owner under tenancy (#3555). These tests pin both
// sides: with tenancy on two accounts each hold their own row for one author
// and see only their own; with tenancy off there is still one row per author
// and the second account's add answers 409 as it always did.

func herbertBooksProvider() *stubMetaProvider {
	return &stubMetaProvider{getBookByID: map[string]*models.Book{
		"OL-DUNE-W": {
			ForeignID: "OL-DUNE-W", Title: "Dune", SortTitle: "Dune", Language: "eng",
			MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary",
		},
		"OL-MESSIAH-W": {
			ForeignID: "OL-MESSIAH-W", Title: "Dune Messiah", SortTitle: "Dune Messiah", Language: "eng",
			MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary",
		},
	}}
}

func herbertBookParams(bookID string) addBookParams {
	return addBookParams{
		ForeignBookID: bookID, ForeignAuthorID: "OL-HERBERT", AuthorName: "Frank Herbert",
		SkipCatalogueSync: true,
	}
}

func TestCreateAuthor_TwoAccountsEachGetTheirOwnRowUnderTenancy(t *testing.T) {
	f := newTenancyFixture(t, true, &stubMetaProvider{})
	ctx := context.Background()
	params := createAuthorParams{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SkipCatalogueSync: true}

	alices, err := f.h.createAuthorCore(f.userCtx(f.alice), params)
	if err != nil {
		t.Fatalf("alice's add: %v", err)
	}
	bobs, err := f.h.createAuthorCore(f.userCtx(f.bob), params)
	if err != nil {
		t.Fatalf("bob's add of an author alice holds: %v", err)
	}
	if alices.Author.ID == bobs.Author.ID {
		t.Fatalf("both accounts got author %d, want a row each", alices.Author.ID)
	}
	if alices.Author.OwnerUserID != f.alice || bobs.Author.OwnerUserID != f.bob {
		t.Fatalf("owners = %d, %d; want alice %d, bob %d",
			alices.Author.OwnerUserID, bobs.Author.OwnerUserID, f.alice, f.bob)
	}

	for _, tc := range []struct {
		name   string
		user   int64
		wantID int64
	}{
		{"alice", f.alice, alices.Author.ID},
		{"bob", f.bob, bobs.Author.ID},
	} {
		list, err := f.authors.ListByUser(ctx, tc.user)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != tc.wantID {
			t.Errorf("%s's author list = %+v, want only author %d", tc.name, list, tc.wantID)
		}
		got, err := f.authors.GetByAnyForeignIDForUser(ctx, "OL-HERBERT", tc.user)
		if err != nil || got == nil || got.ID != tc.wantID {
			t.Errorf("%s's lookup = %+v (err %v), want author %d", tc.name, got, err, tc.wantID)
		}
		if other, _ := f.authors.GetByIDForUser(ctx, alices.Author.ID+bobs.Author.ID-tc.wantID, tc.user); other != nil {
			t.Errorf("%s can read the other account's author: %+v", tc.name, other)
		}
	}

	// A third add by alice is still a conflict, naming her own row.
	_, err = f.h.createAuthorCore(f.userCtx(f.alice), params)
	var conflict *authorConflictError
	if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != alices.Author.ID {
		t.Fatalf("alice's second add = %v, want a conflict naming her author %d", err, alices.Author.ID)
	}
}

// The issue's repro: bob holds Frank Herbert, alice adds a Herbert book nobody
// holds. She used to get 409; now the book lands under her own Herbert, and
// bob's author and books are untouched.
func TestAddBook_BookByAuthorAnotherAccountHoldsUnderTenancy(t *testing.T) {
	f := newTenancyFixture(t, true, herbertBooksProvider())
	ctx := context.Background()

	bobsBook, err := f.h.addBookCore(f.userCtx(f.bob), herbertBookParams("OL-MESSIAH-W"))
	if err != nil {
		t.Fatalf("bob's add: %v", err)
	}
	alicesBook, err := f.h.addBookCore(f.userCtx(f.alice), herbertBookParams("OL-DUNE-W"))
	if err != nil {
		t.Fatalf("alice's add of a book by an author bob holds: %v", err)
	}

	if bobsBook.Book.AuthorID == alicesBook.Book.AuthorID {
		t.Fatalf("both books filed under author %d, want one Herbert each", bobsBook.Book.AuthorID)
	}
	alicesAuthor, _ := f.authors.GetByID(ctx, alicesBook.Book.AuthorID)
	bobsAuthor, _ := f.authors.GetByID(ctx, bobsBook.Book.AuthorID)
	if alicesAuthor == nil || alicesAuthor.OwnerUserID != f.alice || alicesAuthor.ForeignID != "OL-HERBERT" {
		t.Fatalf("alice's author = %+v, want her own OL-HERBERT row", alicesAuthor)
	}
	if bobsAuthor == nil || bobsAuthor.OwnerUserID != f.bob {
		t.Fatalf("bob's author = %+v, want it still his", bobsAuthor)
	}
	if alicesBook.Book.OwnerUserID != f.alice || bobsBook.Book.OwnerUserID != f.bob {
		t.Fatalf("book owners = alice %d, bob %d", alicesBook.Book.OwnerUserID, bobsBook.Book.OwnerUserID)
	}
	for _, tc := range []struct {
		name     string
		authorID int64
		want     string
	}{
		{"alice", alicesAuthor.ID, "OL-DUNE-W"},
		{"bob", bobsAuthor.ID, "OL-MESSIAH-W"},
	} {
		books, err := f.books.ListByAuthor(ctx, tc.authorID)
		if err != nil {
			t.Fatal(err)
		}
		if len(books) != 1 || books[0].ForeignID != tc.want {
			t.Errorf("%s's Herbert holds %+v, want only %s", tc.name, books, tc.want)
		}
	}
}

// Tenancy off is one shared library: one row per author, whoever added it,
// and a second account's Add Author answers 409 naming the existing row.
func TestCreateAuthor_TenancyOffKeepsOneRowPerAuthor(t *testing.T) {
	f := newTenancyFixture(t, false, &stubMetaProvider{})
	ctx := context.Background()
	params := createAuthorParams{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SkipCatalogueSync: true}

	alices, err := f.h.createAuthorCore(f.userCtx(f.alice), params)
	if err != nil {
		t.Fatalf("alice's add: %v", err)
	}
	_, err = f.h.createAuthorCore(f.userCtx(f.bob), params)
	var conflict *authorConflictError
	if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != alices.Author.ID {
		t.Fatalf("bob's add = %v, want a conflict naming alice's author %d", err, alices.Author.ID)
	}
	rec := httptest.NewRecorder()
	f.h.writeCreateAuthorError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", nil), err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	all, _ := f.authors.ListByUser(ctx, 0)
	if len(all) != 1 {
		t.Fatalf("library has %d authors, want 1", len(all))
	}
}

// With tenancy off the repository itself still refuses a second row for a
// provider id, even from a caller that skips the handler's lookup. The unique
// index only covers one owner, so this is the guard that keeps the single
// shared row.
func TestAuthorRepo_TenancyOffRefusesSecondOwnersRow(t *testing.T) {
	f := newTenancyFixture(t, false, &stubMetaProvider{})
	ctx := context.Background()
	a := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, a, f.alice); err != nil {
		t.Fatal(err)
	}
	b := &models.Author{ForeignID: "OL-HERBERT", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(ctx, b, f.bob); !isAuthorIdentityConflict(err) {
		t.Fatalf("second owner's create = %v, want an identity conflict", err)
	}
	all, _ := f.authors.ListByUser(ctx, 0)
	if len(all) != 1 {
		t.Fatalf("library has %d authors, want 1", len(all))
	}
}

// An add whose provider id is taken by an identifier row with no author is a
// plain 409 in every mode. It used to say "another account's library" for a
// scoped caller, which is never the cause now.
func TestCreateAuthor_OrphanIdentifierIsPlainConflict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
	}{
		{"tenancy off", false, false},
		{"admin under tenancy", true, true},
		{"non admin under tenancy", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, &stubMetaProvider{})
			seedOrphanIdentifier(t, f, "OL-ORPHAN")

			_, err := f.h.createAuthorCore(f.callerCtx(tc.admin), createAuthorParams{
				ForeignID: "OL-ORPHAN", Name: "Orphan Author", SkipCatalogueSync: true,
			})
			if err == nil {
				t.Fatal("expected a conflict")
			}
			rec := httptest.NewRecorder()
			f.h.writeCreateAuthorError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", nil), err)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if msg, _ := body["error"].(string); msg != "author already exists" {
				t.Errorf("error = %q, want %q", msg, "author already exists")
			}
		})
	}
}

// A request for an author is refused as already in the library only when the
// requester can see that author. Under tenancy another account's copy does
// not count, since approving gives the requester their own (#3555); with
// tenancy off the library is shared and any copy counts, as before.
func TestRequestAuthor_InLibraryCheckFollowsTenancy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		want    int
	}{
		{"tenancy on, another account holds it", true, http.StatusCreated},
		{"tenancy off, another account holds it", false, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tc.tenancy)
			f := newRequestsFixture(t, wellsRequestStub(), nil)
			a := &models.Author{ForeignID: "OL39307A", Name: "H. G. Wells", SortName: "Wells, H. G.", MetadataProvider: "openlibrary"}
			if err := f.authors.CreateForUser(context.Background(), a, f.other.ID); err != nil {
				t.Fatal(err)
			}
			rec := f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`)
			if rec.Code != tc.want {
				t.Fatalf("status = %d (%q), want %d", rec.Code, requestErrorBody(rec), tc.want)
			}
		})
	}

	// The requester's own copy still counts under tenancy.
	auth.SetEnforceTenancyForTests(t, true)
	f := newRequestsFixture(t, wellsRequestStub(), nil)
	a := &models.Author{ForeignID: "OL39307A", Name: "H. G. Wells", SortName: "Wells, H. G.", MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(context.Background(), a, f.requester.ID); err != nil {
		t.Fatal(err)
	}
	rec := f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`)
	if rec.Code != http.StatusConflict || requestErrorBody(rec) != "This author is already in the library." {
		t.Fatalf("own copy: %d %q, want 409 already in the library", rec.Code, requestErrorBody(rec))
	}
}

// seedOrphanIdentifier plants an author_identifiers row whose author is gone,
// which still blocks the id (as the old UNIQUE did) while no author row is
// visible to anyone.
func seedOrphanIdentifier(t *testing.T, f tenancyFixture, foreignID string) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO author_identifiers (author_id, provider, foreign_id) VALUES (999999, 'openlibrary', ?)`, foreignID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}
