package bookorbit

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// bookOrbitIDs lists the BookOrbit book ids Bindery remembers for a book.
func (s *shelf) bookOrbitIDs(bookID int64) []string {
	s.t.Helper()
	links, err := db.NewABSProvenanceRepo(s.database).ListByLocal(context.Background(), ProvenanceBookEntity, bookID)
	if err != nil {
		s.t.Fatal(err)
	}
	var out []string
	for _, l := range links {
		if l.SourceID == SourceID {
			out = append(out, l.ExternalID)
		}
	}
	return out
}

func (s *shelf) addSalt(id int64) {
	s.fake.addBook(2, fakeBook(id, "The Salt Road", []string{"Ursula Vale"},
		fakeFile(1, s.put("Fantasy/Ursula Vale/The Salt Road/The Salt Road.epub"), "primary")))
}

func TestImportStoresBookOrbitIDOnCreate(t *testing.T) {
	s := newShelf(t)
	s.addSalt(61)
	s.run(s.config(false))
	book := s.bookByTitle("The Salt Road")
	if got := s.bookOrbitIDs(book.ID); len(got) != 1 || got[0] != "61" {
		t.Fatalf("stored ids = %v, want [61]", got)
	}
}

func TestImportStoresBookOrbitIDOnMatch(t *testing.T) {
	s := newShelf(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL123A", Name: "Ursula Vale", SortName: "Vale, Ursula", MetadataProvider: "openlibrary", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Book{ForeignID: "OL1W", AuthorID: author.ID, Title: "The Salt Road", SortTitle: "Salt Road", Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook}
	if err := s.books.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	s.addSalt(62)
	stats := s.run(s.config(false))
	if stats.BooksCreated != 0 {
		t.Fatalf("expected a match, got %s", stats)
	}
	if got := s.bookOrbitIDs(existing.ID); len(got) != 1 || got[0] != "62" {
		t.Fatalf("stored ids = %v, want [62]", got)
	}
}

func TestImportDoesNotStoreBookOrbitIDOnDryRun(t *testing.T) {
	s := newShelf(t)
	s.addSalt(61)
	s.run(s.config(true))
	if n := s.count("abs_provenance"); n != 0 {
		t.Fatalf("dry run wrote %d provenance rows", n)
	}
}

func TestImportDoesNotStoreBookOrbitIDOnAmbiguousMatch(t *testing.T) {
	s := newShelf(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL123A", Name: "Ursula Vale", SortName: "Vale, Ursula", MetadataProvider: "openlibrary", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, fid := range []string{"OL1W", "OL2W"} {
		b := &models.Book{ForeignID: fid, AuthorID: author.ID, Title: "The Salt Road", SortTitle: "Salt Road", Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook}
		if err := s.books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	s.addSalt(61)
	stats := s.run(s.config(false))
	if stats.Failed != 1 {
		t.Fatalf("expected the item to be held as ambiguous, got %s", stats)
	}
	for _, id := range ids {
		if got := s.bookOrbitIDs(id); len(got) != 0 {
			t.Errorf("ambiguous match stored %v on book %d", got, id)
		}
	}
}

func TestImportStoresNothingWhenBookOrbitLacksTheBook(t *testing.T) {
	s := newShelf(t)
	s.run(s.config(false))
	if n := s.count("abs_provenance"); n != 0 {
		t.Fatalf("stored %d rows for an empty BookOrbit", n)
	}
}

func TestImportOverwritesTheStoredIDAfterARebuild(t *testing.T) {
	s := newShelf(t)
	s.addSalt(61)
	s.run(s.config(false))
	book := s.bookByTitle("The Salt Road")

	// BookOrbit was rebuilt and numbers the same book 90 now.
	s.fake.books[2] = nil
	s.addSalt(90)
	s.run(s.config(false))

	if s.count("books") != 1 {
		t.Fatalf("rebuild minted %d books", s.count("books"))
	}
	links, err := db.NewABSProvenanceRepo(s.database).ListByLocal(context.Background(), ProvenanceBookEntity, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var newest *models.ABSProvenance
	for i := range links {
		if links[i].ExternalID == "90" {
			newest = &links[i]
		}
	}
	if newest == nil {
		t.Fatalf("no link for the new id: %+v", links)
	}
	for _, l := range links {
		if l.ExternalID == "61" && l.UpdatedAt.After(newest.UpdatedAt) {
			t.Errorf("old id is newer than the new one")
		}
	}
}
