package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type rebindEditionsFixture struct {
	h         *BookHandler
	books     *db.BookRepo
	editions  *db.EditionRepo
	downloads *db.DownloadRepo
	book      *models.Book
	ctx       context.Context
}

func newRebindEditionsFixture(t *testing.T) rebindEditionsFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	editions := db.NewEditionRepo(database)
	author := &models.Author{
		ForeignID: "OL1A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "Wrong Book", SortTitle: "Wrong Book",
		Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Monitored: true, Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).
		WithAuthors(authors).WithSeries(db.NewSeriesRepo(database)).WithEditionHydration(editions).
		WithMetaLookup(&stubLookup{book: &models.Book{
			Title: "Correct Book", SortTitle: "Correct Book",
			Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
		}})
	return rebindEditionsFixture{h: h, books: books, editions: editions, downloads: db.NewDownloadRepo(database), book: book, ctx: ctx}
}

// addProviderEdition persists a metadata provider edition the way hydration
// does, against the book's identity at the time.
func (f rebindEditionsFixture) addProviderEdition(t *testing.T, foreignID, isbn string) *models.Edition {
	t.Helper()
	e := &models.Edition{ForeignID: foreignID, BookID: f.book.ID, Title: "Wrong Book", ISBN13: &isbn, Monitored: true}
	current, err := f.books.GetByID(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := f.editions.UpsertMetadata(f.ctx, e, current.ForeignID, current.MetadataProvider)
	if err != nil || !ok {
		t.Fatalf("UpsertMetadata(%s) = %v, %v", foreignID, ok, err)
	}
	return e
}

func (f rebindEditionsFixture) rebind(t *testing.T, foreignID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.Rebind(rec, rebindRequest(f.book.ID, map[string]any{"provider": "openlibrary", "foreign_id": foreignID}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rebind: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func (f rebindEditionsFixture) editionIDs(t *testing.T) []string {
	t.Helper()
	editions, err := f.editions.ListByBook(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(editions))
	for _, e := range editions {
		out = append(out, e.ForeignID)
	}
	sort.Strings(out)
	return out
}

// The sequence from the report: a metadata edition of the wrong work is
// persisted, then the book is rebound. The old edition, and with it the old
// work's ISBN that search would use, must not survive (#2781).
func TestRebind_DiscardsPreviousWorksProviderEditions(t *testing.T) {
	f := newRebindEditionsFixture(t)
	f.addProviderEdition(t, "OL-old-edition", "9780140328721")

	f.rebind(t, "OL2W")

	if got := f.editionIDs(t); len(got) != 0 {
		t.Fatalf("editions after rebind = %v, want the previous work's provider edition gone", got)
	}
}

// What rebind keeps: editions the importers created for the user's files, the
// edition the user selected, an edition a download recorded, and every
// edition when the rebind does not change the identity at all.
func TestRebind_KeepsOwnedAndReferencedEditions(t *testing.T) {
	f := newRebindEditionsFixture(t)
	for _, id := range []string{"calibre:edition:7:EPUB", "abs:edition:lib:item"} {
		isbn := "9780000000001"
		if err := f.editions.Upsert(f.ctx, &models.Edition{ForeignID: id, BookID: f.book.ID, Title: "Wrong Book", ISBN13: &isbn, Monitored: true}); err != nil {
			t.Fatal(err)
		}
	}
	f.addProviderEdition(t, "OL-unreferenced", "9780000000002")
	selected := f.addProviderEdition(t, "OL-selected", "9780000000003")
	grabbed := f.addProviderEdition(t, "OL-grabbed", "9780000000004")

	book, err := f.books.GetByID(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	book.SelectedEditionID = &selected.ID
	if err := f.books.Update(f.ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := f.downloads.Create(f.ctx, &models.Download{
		GUID: "grab-1", BookID: &book.ID, EditionID: &grabbed.ID, Title: "Wrong Book", Status: models.DownloadStatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}

	// Same identity: nothing is invalidated.
	f.rebind(t, "OL1W")
	if got := f.editionIDs(t); len(got) != 5 {
		t.Fatalf("editions after a same-work rebind = %v, want all five kept", got)
	}

	f.rebind(t, "OL2W")
	want := []string{"OL-grabbed", "OL-selected", "abs:edition:lib:item", "calibre:edition:7:EPUB"}
	got := f.editionIDs(t)
	if len(got) != len(want) {
		t.Fatalf("editions after rebind = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("editions after rebind = %v, want %v", got, want)
		}
	}
}
