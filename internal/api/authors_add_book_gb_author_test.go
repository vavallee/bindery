package api

import (
	"bytes"
	"context"
	"database/sql"
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

// olAuthorSearchProvider stands in for OpenLibrary during the author name
// lookup: it answers the search with whatever it is given (nothing, or an
// error, or a record), the ways the canonical lookup comes back empty handed.
type olAuthorSearchProvider struct {
	stubMetaProvider
	searchErr error
	authors   []models.Author
}

func (p *olAuthorSearchProvider) SearchAuthors(_ context.Context, _ string) ([]models.Author, error) {
	return p.authors, p.searchErr
}

type gbAddFixture struct {
	db      *sql.DB
	h       *AuthorHandler
	authors *db.AuthorRepo
	books   *db.BookRepo
	gb      *stubMetaProvider
}

// newGBAddFixture builds an AddBook handler whose only knowledge is Google
// Books volumes, plus an OpenLibrary that has no author by that name. This is
// the #3538 shape: no ISBN, no canonical author anywhere.
func newGBAddFixture(t *testing.T, searchErr error, volumes ...*models.Book) *gbAddFixture {
	t.Helper()
	return newGBAddFixtureOL(t, &olAuthorSearchProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}, searchErr: searchErr}, volumes...)
}

// newGBAddFixtureOL is newGBAddFixture with the OpenLibrary stand in given, or
// nil for an install where OpenLibrary is not in the provider set at all.
func newGBAddFixtureOL(t *testing.T, ol *olAuthorSearchProvider, volumes ...*models.Book) *gbAddFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)

	byID := map[string]*models.Book{}
	for _, v := range volumes {
		byID[v.ForeignID] = v
	}
	gb := &stubMetaProvider{name: "googlebooks", getBookByID: byID}
	var agg *metadata.Aggregator
	if ol == nil {
		agg = metadata.NewAggregator(gb)
	} else {
		agg = metadata.NewAggregator(gb, ol)
	}

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	h := NewAuthorHandler(authors, nil, books, nil, agg, nil, db.NewMetadataProfileRepo(database), nil)
	return &gbAddFixture{db: database, h: h, authors: authors, books: books, gb: gb}
}

func gbVolume(id, title string) *models.Book {
	return &models.Book{
		ForeignID: id, Title: title, SortTitle: title, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "googlebooks",
	}
}

func (f *gbAddFixture) add(ctx context.Context, bookID, authorName string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"foreignBookId": bookID, "authorName": authorName})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.h.AddBook(rec, req)
	return rec
}

// #3538: a Google Books title that OpenLibrary, Hardcover and DNB know nothing
// about used to 422 with advice that could not work. It now creates the author
// under a synthetic gb:author:<slug> id and adds the book.
func TestAddBook_GoogleBooksOnlyTitleCreatesSyntheticAuthor(t *testing.T) {
	f := newGBAddFixture(t, nil, gbVolume("gb:S2IcEgAAQBAJ", "Pass the NMC CBT"))
	ctx := context.Background()

	rec := f.add(ctx, "gb:S2IcEgAAQBAJ", "Owen Brackett")
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	author, err := f.authors.GetByForeignID(ctx, "gb:author:owen-brackett")
	if err != nil || author == nil {
		t.Fatalf("synthetic author not created: err=%v author=%v", err, author)
	}
	if author.Name != "Owen Brackett" {
		t.Errorf("author name = %q, want the volume's name", author.Name)
	}
	if author.MetadataProvider != "googlebooks" {
		t.Errorf("author metadata_provider = %q, want googlebooks", author.MetadataProvider)
	}
	book, err := f.books.GetByForeignID(ctx, "gb:S2IcEgAAQBAJ")
	if err != nil || book == nil {
		t.Fatalf("book not persisted: err=%v book=%v", err, book)
	}
	if book.AuthorID != author.ID {
		t.Errorf("book author = %d, want %d", book.AuthorID, author.ID)
	}
}

// A second Google Books only title by the same author lands on the same row,
// however the name is cased or spaced, with no catalogue sync needed to link it.
func TestAddBook_GoogleBooksOnlySecondTitleReusesAuthor(t *testing.T) {
	f := newGBAddFixture(t, nil,
		gbVolume("gb:AAAAAAAAAAAA", "First Book"),
		gbVolume("gb:BBBBBBBBBBBB", "Second Book"))
	ctx := context.Background()

	if rec := f.add(ctx, "gb:AAAAAAAAAAAA", "Owen Brackett"); rec.Code != http.StatusCreated {
		t.Fatalf("first add: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.add(ctx, "gb:BBBBBBBBBBBB", "  owen   BRACKETT "); rec.Code != http.StatusCreated {
		t.Fatalf("second add: %d %s", rec.Code, rec.Body.String())
	}
	all, err := f.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected one author, got %d", len(all))
	}
	for _, id := range []string{"gb:AAAAAAAAAAAA", "gb:BBBBBBBBBBBB"} {
		b, err := f.books.GetByForeignID(ctx, id)
		if err != nil || b == nil || b.AuthorID != all[0].ID {
			t.Errorf("book %s not linked to the shared author: err=%v book=%v", id, err, b)
		}
	}
}

// Names with nothing usable in them cannot make an id, so they refuse as
// before instead of sharing one empty key.
func TestAddBook_GoogleBooksOnlyUnusableNamesStillRefuse(t *testing.T) {
	for _, name := range []string{"", "   ", "...", "-- !! --"} {
		f := newGBAddFixture(t, nil, gbVolume("gb:NAMELESS0001", "Nameless"))
		rec := f.add(context.Background(), "gb:NAMELESS0001", name)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("name %q: expected 422, got %d: %s", name, rec.Code, rec.Body.String())
		}
		all, _ := f.authors.List(context.Background())
		if len(all) != 0 {
			t.Errorf("name %q: an author was created anyway: %+v", name, all)
		}
	}
}

// A canonical lookup that FAILED proves nothing about the author, so it must
// not be turned into a synthetic author who could later be a duplicate of the
// real OpenLibrary one.
func TestAddBook_GoogleBooksOnlyRefusesWhenCanonicalLookupFails(t *testing.T) {
	f := newGBAddFixture(t, errors.New("openlibrary: timeout"), gbVolume("gb:S2IcEgAAQBAJ", "Pass the NMC CBT"))
	rec := f.add(context.Background(), "gb:S2IcEgAAQBAJ", "Owen Brackett")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	all, _ := f.authors.List(context.Background())
	if len(all) != 0 {
		t.Errorf("an author was created on a failed lookup: %+v", all)
	}
}

// Only Google Books results get a synthetic author. Any other provider's
// record with no author still refuses, as before.
func TestAddBook_NonGoogleBooksNoAuthorStillRefuses(t *testing.T) {
	f := newGBAddFixture(t, nil)
	rec := f.add(context.Background(), "OL999W", "Owen Brackett")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Under tenancy the synthetic author is owned like any other provider's: the
// row belongs to the user who added it, a second user never sees it or gets a
// book attached to it, and the second add answers 409 exactly as it does for a
// shared OpenLibrary author (authors.foreign_id is unique across users).
func TestAddBook_GoogleBooksOnlyAuthorIsScopedPerUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newGBAddFixture(t, nil,
		gbVolume("gb:AAAAAAAAAAAA", "First Book"),
		gbVolume("gb:BBBBBBBBBBBB", "Second Book"))
	ctx := context.Background()

	users := db.NewUserRepo(f.db)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "h2")
	if err != nil {
		t.Fatal(err)
	}

	if rec := f.add(auth.WithUserID(ctx, alice.ID), "gb:AAAAAAAAAAAA", "Owen Brackett"); rec.Code != http.StatusCreated {
		t.Fatalf("alice add: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.add(auth.WithUserID(ctx, bob.ID), "gb:BBBBBBBBBBBB", "Owen Brackett")
	if rec.Code != http.StatusConflict {
		t.Errorf("bob add: expected 409 like any other provider's author, got %d: %s", rec.Code, rec.Body.String())
	}

	mine, err := f.authors.GetByForeignIDForUser(ctx, "gb:author:owen-brackett", alice.ID)
	if err != nil || mine == nil || mine.OwnerUserID != alice.ID {
		t.Fatalf("alice's author missing or wrongly owned: err=%v author=%+v", err, mine)
	}
	if theirs, _ := f.authors.GetByForeignIDForUser(ctx, "gb:author:owen-brackett", bob.ID); theirs != nil {
		t.Errorf("bob can see alice's author: %+v", theirs)
	}
	if b, _ := f.books.GetByForeignID(ctx, "gb:BBBBBBBBBBBB"); b != nil {
		t.Errorf("bob's refused add left a book behind: %+v", b)
	}
}

// OpenLibrary not being in the provider set says nothing about the author, so
// the add must keep refusing instead of minting an author OpenLibrary might
// know (a Hardcover primary install with OpenLibrary off, for one).
func TestAddBook_GoogleBooksOnlyRefusesWhenOpenLibraryNotConsulted(t *testing.T) {
	f := newGBAddFixtureOL(t, nil, gbVolume("gb:S2IcEgAAQBAJ", "Pass the NMC CBT"))
	rec := f.add(context.Background(), "gb:S2IcEgAAQBAJ", "Stephen King")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if all, _ := f.authors.List(context.Background()); len(all) != 0 {
		t.Errorf("an author was created without asking OpenLibrary: %+v", all)
	}
}

// An OpenLibrary record with the same name but no works is not trusted as the
// author, and is not proof of absence either: it is probably the real person
// under a thin record, and a synthetic author beside it would be a duplicate.
func TestAddBook_GoogleBooksOnlyRefusesWhenOpenLibraryHasEmptySameNameRecord(t *testing.T) {
	ol := &olAuthorSearchProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		authors:          []models.Author{{ForeignID: "OL1A", Name: "Owen Brackett", Statistics: &models.AuthorStats{BookCount: 0}}},
	}
	f := newGBAddFixtureOL(t, ol, gbVolume("gb:S2IcEgAAQBAJ", "Pass the NMC CBT"))
	rec := f.add(context.Background(), "gb:S2IcEgAAQBAJ", "Owen Brackett")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if all, _ := f.authors.List(context.Background()); len(all) != 0 {
		t.Errorf("an author was created next to an OpenLibrary record: %+v", all)
	}
}

// OpenLibrary answering with other people (fuzzy search hits) is "no match":
// only a record carrying the same name blocks the synthetic author. A spelling
// variant that does not normalise to the same name cannot be told apart from
// a different person, so it is treated as a different person (documented).
func TestAddBook_GoogleBooksOnlyIgnoresUnrelatedOpenLibraryHits(t *testing.T) {
	ol := &olAuthorSearchProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		authors:          []models.Author{{ForeignID: "OL2A", Name: "Owen Brackenridge", Statistics: &models.AuthorStats{BookCount: 40}}},
	}
	f := newGBAddFixtureOL(t, ol, gbVolume("gb:S2IcEgAAQBAJ", "Pass the NMC CBT"))
	rec := f.add(context.Background(), "gb:S2IcEgAAQBAJ", "Owen Brackett")
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}
