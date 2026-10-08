package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// tenancyFixture is two users, alice and bob, plus an admin, on a fresh in
// memory database, with an AuthorHandler over the given provider. The cross
// user Add Book and Add Author tests below share it.
type tenancyFixture struct {
	h       *AuthorHandler
	authors *db.AuthorRepo
	books   *db.BookRepo
	aliases *db.AuthorAliasRepo
	alice   int64
	bob     int64
	admin   int64
}

func newTenancyFixture(t *testing.T, tenancy bool, provider metadata.Provider) tenancyFixture {
	t.Helper()
	auth.SetEnforceTenancyForTests(t, tenancy)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	users := db.NewUserRepo(database)
	ids := make([]int64, 0, 3)
	for _, name := range []string{"alice", "bob", "admin"} {
		u, err := users.Create(ctx, name, "h-"+name)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	aliasRepo := db.NewAuthorAliasRepo(database)
	h := NewAuthorHandler(authorRepo, aliasRepo, bookRepo, nil,
		metadata.NewAggregator(provider), nil, db.NewMetadataProfileRepo(database), nil)
	return tenancyFixture{h: h, authors: authorRepo, books: bookRepo, aliases: aliasRepo,
		alice: ids[0], bob: ids[1], admin: ids[2]}
}

func (f tenancyFixture) userCtx(id int64) context.Context {
	return auth.WithUserID(context.Background(), id)
}

func (f tenancyFixture) adminCtx() context.Context {
	return auth.WithUserRole(auth.WithUserID(context.Background(), f.admin), "admin")
}

// seedSharedAuthorWithAliceStub files a calibre stub of "Dune", owned by
// alice, under an author with no owner. A NULL owned author is shared (any
// local only or API key request creates one), but the books under it are not.
func (f tenancyFixture) seedSharedAuthorWithAliceStub(t *testing.T) *models.Book {
	t.Helper()
	ctx := context.Background()
	shared := &models.Author{
		ForeignID: "OL-SHARED-A", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary",
	}
	if err := f.authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	stub := &models.Book{
		ForeignID: "calibre:42", Title: "Dune", SortTitle: "Dune", AuthorID: shared.ID,
		Status: models.BookStatusImported, Monitored: false, Genres: []string{},
		MediaType: models.MediaTypeEbook, MetadataProvider: "calibre", OwnerUserID: f.alice,
	}
	if err := f.books.Create(ctx, stub); err != nil {
		t.Fatal(err)
	}
	before, err := f.books.GetByID(ctx, stub.ID)
	if err != nil || before == nil {
		t.Fatalf("re-read alice's stub: %+v err=%v", before, err)
	}
	return before
}

func duneAudiobookProvider() *stubMetaProvider {
	return &stubMetaProvider{getBookByID: map[string]*models.Book{
		"OL-DUNE-W": {
			ForeignID: "OL-DUNE-W", Title: "Dune", SortTitle: "Dune", Language: "eng",
			MediaType: models.MediaTypeAudiobook, Genres: []string{}, MetadataProvider: "openlibrary",
		},
	}}
}

var duneAddParams = addBookParams{
	ForeignBookID: "OL-DUNE-W", ForeignAuthorID: "OL-SHARED-A", AuthorName: "Frank Herbert",
	SkipCatalogueSync: true,
}

func assertBookUnchanged(t *testing.T, books *db.BookRepo, before *models.Book) {
	t.Helper()
	after, err := books.GetByID(context.Background(), before.ID)
	if err != nil || after == nil {
		t.Fatalf("re-read book %d: %+v err=%v", before.ID, after, err)
	}
	if after.ForeignID != before.ForeignID || after.MediaType != before.MediaType ||
		after.Monitored != before.Monitored || after.Status != before.Status ||
		after.Language != before.Language || after.OwnerUserID != before.OwnerUserID ||
		after.AuthorID != before.AuthorID || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("another user's add changed the row:\n before %+v\n after  %+v", before, after)
	}
}

// With tenancy on, a title match under a shared author must not be adopted
// when another user owns it. The adopt rewrites the row's foreign id and
// widens its format, and the guard after the poll only refuses the request
// once that write has already landed in alice's library.
func TestAddBook_TenancyDoesNotAdoptAnotherUsersTitleMatch(t *testing.T) {
	f := newTenancyFixture(t, true, duneAudiobookProvider())
	before := f.seedSharedAuthorWithAliceStub(t)

	res, err := f.h.addBookCore(f.userCtx(f.bob), duneAddParams)
	if err != nil {
		t.Fatalf("bob's add: %v", err)
	}
	assertBookUnchanged(t, f.books, before)
	if res.Book == nil || res.Book.ID == before.ID {
		t.Fatalf("bob's add resolved to alice's row: %+v", res.Book)
	}
	// The author is shared, but the book bob asked for is his: a row with no
	// owner would show up in alice's library beside her own copy.
	if res.Book.OwnerUserID != f.bob {
		t.Fatalf("bob's new row owner = %d, want bob (%d)", res.Book.OwnerUserID, f.bob)
	}
}

// The admin is refused another user's row by the guard after the poll, so the
// adopt must not touch that row for the admin either.
func TestAddBook_TenancyAdminDoesNotAdoptAnotherUsersTitleMatch(t *testing.T) {
	f := newTenancyFixture(t, true, duneAudiobookProvider())
	before := f.seedSharedAuthorWithAliceStub(t)

	res, err := f.h.addBookCore(f.adminCtx(), duneAddParams)
	if err != nil {
		t.Fatalf("admin's add: %v", err)
	}
	assertBookUnchanged(t, f.books, before)
	if res.Book == nil || res.Book.ID == before.ID {
		t.Fatalf("admin's add resolved to alice's row: %+v", res.Book)
	}
	// An admin's add under a shared author stays shared, as before.
	if res.Book.OwnerUserID != 0 {
		t.Fatalf("admin's new row owner = %d, want none", res.Book.OwnerUserID)
	}
}

// When the provider's book endpoint fails, Add Book falls back to a single
// work catalogue sync. Under a shared (unowned) author that sync must give the
// book to the caller, exactly as the direct insert does, and leave an admin's
// book unowned as before.
func TestAddBook_TenancyFallbackSyncUnderSharedAuthorOwnsBook(t *testing.T) {
	for _, tc := range []struct {
		name      string
		admin     bool
		wantOwner func(f tenancyFixture) int64
	}{
		{"user", false, func(f tenancyFixture) int64 { return f.bob }},
		{"admin", true, func(tenancyFixture) int64 { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubMetaProvider{
				getBookErrByID: map[string]error{"OL-SOLO-W": errors.New("provider 502")},
				works: []models.Book{{
					ForeignID: "OL-SOLO-W", Title: "Solo Work", SortTitle: "Solo Work", Language: "eng",
					MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary",
				}},
			}
			f := newTenancyFixture(t, true, provider)
			shared := &models.Author{
				ForeignID: "OL-SHARED-A", Name: "Shared Writer", SortName: "Writer, Shared",
				MetadataProvider: "openlibrary",
			}
			if err := f.authors.Create(context.Background(), shared); err != nil {
				t.Fatal(err)
			}
			ctx := f.userCtx(f.bob)
			if tc.admin {
				ctx = f.adminCtx()
			}
			res, err := f.h.addBookCore(ctx, addBookParams{
				ForeignBookID: "OL-SOLO-W", ForeignAuthorID: "OL-SHARED-A", AuthorName: "Shared Writer",
			})
			if err != nil {
				t.Fatalf("add: %v", err)
			}
			if res.Book == nil || res.Book.AuthorID != shared.ID {
				t.Fatalf("add = %+v, want a book under the shared author", res.Book)
			}
			if want := tc.wantOwner(f); res.Book.OwnerUserID != want {
				t.Fatalf("fallback sync's book owner = %d, want %d", res.Book.OwnerUserID, want)
			}
		})
	}
}

// Tenancy off keeps the shared library: the title match is adopted exactly as
// before (dual format upgrade, stub rebound to the provider id).
func TestAddBook_TenancyOffStillAdoptsTitleMatch(t *testing.T) {
	f := newTenancyFixture(t, false, duneAudiobookProvider())
	before := f.seedSharedAuthorWithAliceStub(t)

	res, err := f.h.addBookCore(context.Background(), duneAddParams)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Book == nil || res.Book.ID != before.ID {
		t.Fatalf("add did not adopt the title match: %+v", res.Book)
	}
	if res.Book.ForeignID != "OL-DUNE-W" || res.Book.MediaType != models.MediaTypeBoth {
		t.Fatalf("adopted row = foreignId %q mediaType %q, want OL-DUNE-W both", res.Book.ForeignID, res.Book.MediaType)
	}
}

// Bob asking for a work alice holds is refused before anything is written or
// fetched: no author row for bob, not even one the orphan cleanup later
// removes, and no provider call. The 409 carries nothing of alice's row.
func TestAddBook_TenancyRefusesAnotherUsersBookBeforeAnySideEffect(t *testing.T) {
	authorCalls := make(chan bool, 8)
	bookCalls := make(chan struct{}, 8)
	provider := &stubMetaProvider{getAuthorBypass: authorCalls, getBookEntered: bookCalls}
	f := newTenancyFixture(t, true, provider)
	ctx := context.Background()
	aliceAuthor := &models.Author{
		ForeignID: "OL-ALICE-A", Name: "Alice Author", SortName: "Author, Alice",
		MetadataProvider: "openlibrary",
	}
	if err := f.authors.CreateForUser(ctx, aliceAuthor, f.alice); err != nil {
		t.Fatal(err)
	}
	owned := &models.Book{
		ForeignID: "OL-ALICE-W", Title: "Owned", SortTitle: "Owned", AuthorID: aliceAuthor.ID,
		Status: models.BookStatusImported, Monitored: false, Genres: []string{},
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary", OwnerUserID: f.alice,
		ImageURL: "https://example.invalid/alice-cover.jpg",
	}
	if err := f.books.Create(ctx, owned); err != nil {
		t.Fatal(err)
	}
	before, _ := f.books.GetByID(ctx, owned.ID)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId": "OL-ALICE-W", "foreignAuthorId": "OL-BOB-A", "authorName": "Bob Author",
		"mediaType": models.MediaTypeBoth,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).
		WithContext(f.userCtx(f.bob))
	rec := httptest.NewRecorder()
	f.h.AddBook(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 || resp["error"] != "book is held by another user" {
		t.Fatalf("409 body = %v, want only the held by another user error", resp)
	}
	for _, leak := range []string{"Owned", "alice-cover", "Alice Author", "OL-ALICE-A"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Fatalf("409 body leaks %q: %s", leak, rec.Body.String())
		}
	}
	assertBookUnchanged(t, f.books, before)
	if n := len(authorCalls); n != 0 {
		t.Fatalf("refused add fetched an author upstream %d time(s)", n)
	}
	if n := len(bookCalls); n != 0 {
		t.Fatalf("refused add fetched the book upstream %d time(s)", n)
	}
	if got, _ := f.authors.GetByForeignID(ctx, "OL-BOB-A"); got != nil {
		t.Fatalf("refused add left an author row for bob: %+v", got)
	}
}

// An author alias belongs to its author, and so to that author's owner. Bob
// creating an author by a name that is only an alias of alice's author must
// not resolve to alice's row: before the fix the 409 carried alice's author
// in full, and for a calibre or audiobookshelf author the request relinked
// alice's row in place, rewriting its identity and monitoring.
func TestCreateAuthor_TenancyIgnoresAnotherUsersAlias(t *testing.T) {
	for _, tc := range []struct {
		name, foreignID, provider string
	}{
		{"linked author", "OL-ALICE-A", "openlibrary"},
		{"relinkable author", "calibre:author:7", "calibre"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubMetaProvider{author: &models.Author{
				ForeignID: "OL-BOB-A", Name: "Pen Name", SortName: "Name, Pen",
				MetadataProvider: "openlibrary",
			}}
			f := newTenancyFixture(t, true, provider)
			ctx := context.Background()
			aliceAuthor := &models.Author{
				ForeignID: tc.foreignID, Name: "Alice Real Name", SortName: "Name, Alice Real",
				MetadataProvider: tc.provider, Monitored: false,
			}
			if err := f.authors.CreateForUser(ctx, aliceAuthor, f.alice); err != nil {
				t.Fatal(err)
			}
			if err := f.aliases.Create(ctx, &models.AuthorAlias{AuthorID: aliceAuthor.ID, Name: "Pen Name"}); err != nil {
				t.Fatal(err)
			}
			before, _ := f.authors.GetByID(ctx, aliceAuthor.ID)

			res, err := f.h.createAuthorCore(f.userCtx(f.bob), createAuthorParams{
				ForeignID: "OL-BOB-A", Name: "Pen Name", Monitored: true, SkipCatalogueSync: true,
			})
			if err != nil {
				var conflict *authorConflictError
				if errors.As(err, &conflict) && conflict.Canonical != nil {
					t.Fatalf("bob's create resolved to alice's author %d: %s", conflict.Canonical.ID, conflict.Message)
				}
				t.Fatalf("bob's create: %v", err)
			}
			if res.Author == nil || res.Author.ID == aliceAuthor.ID || res.Author.OwnerUserID != f.bob {
				t.Fatalf("bob's create = %+v, want a new author owned by bob", res.Author)
			}
			after, _ := f.authors.GetByID(ctx, aliceAuthor.ID)
			if after == nil || after.ForeignID != before.ForeignID || after.Monitored != before.Monitored ||
				after.Name != before.Name || after.OwnerUserID != f.alice {
				t.Fatalf("bob's create changed alice's author:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}

// The admin may see every user's rows, so the alias still resolves for them,
// and tenancy off keeps the shared alias table as it was.
func TestCreateAuthor_AliasStillResolvesForAdminAndTenancyOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenancy bool
		admin   bool
	}{
		{"admin under tenancy", true, true},
		{"tenancy off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTenancyFixture(t, tc.tenancy, &stubMetaProvider{})
			ctx := context.Background()
			aliceAuthor := &models.Author{
				ForeignID: "OL-ALICE-A", Name: "Alice Real Name", SortName: "Name, Alice Real",
				MetadataProvider: "openlibrary",
			}
			if err := f.authors.CreateForUser(ctx, aliceAuthor, f.alice); err != nil {
				t.Fatal(err)
			}
			if err := f.aliases.Create(ctx, &models.AuthorAlias{AuthorID: aliceAuthor.ID, Name: "Pen Name"}); err != nil {
				t.Fatal(err)
			}
			callerCtx := f.userCtx(f.bob)
			if tc.admin {
				callerCtx = f.adminCtx()
			}
			_, err := f.h.createAuthorCore(callerCtx, createAuthorParams{
				ForeignID: "OL-OTHER-A", Name: "Pen Name", SkipCatalogueSync: true,
			})
			var conflict *authorConflictError
			if !errors.As(err, &conflict) || conflict.Canonical == nil || conflict.Canonical.ID != aliceAuthor.ID {
				t.Fatalf("create = %v, want a conflict naming alice's author %d", err, aliceAuthor.ID)
			}
		})
	}
}
