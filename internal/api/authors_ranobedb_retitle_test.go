package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// #2012: a book retitled from RanobeDB keeps its OpenLibrary foreign id, so a
// later author refresh that still sees the work under OpenLibrary's title must
// re-link it by that id and not mint a second row.
func TestFetchAuthorBooks_RanobeDBRetitledBookIsRelinkedByForeignID(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID: "OL3188A", Name: "Isuna Hasekura", SortName: "Hasekura, Isuna",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Stored after RanobeDB retitled it.
	existing := &models.Book{
		ForeignID: "OL3188W", AuthorID: author.ID,
		Title: "Spice and Wolf, Vol. 3", SortTitle: "Spice and Wolf, Vol. 3",
		Language: "eng", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// OpenLibrary still names the same work by its own title.
	stub := &stubMetaProvider{works: []models.Book{{
		ForeignID: "OL3188W", Title: "Ookami to Koushinryou III", SortTitle: "ookami to koushinryou iii",
		Language: "eng", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
	}}}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, nil)
	for range 2 {
		h.FetchAuthorBooks(author, false, "")
	}

	books, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d rows after refreshing, want 1: the retitled book was not re-linked by foreign id", len(books))
	}
	if books[0].Title != "Spice and Wolf, Vol. 3" {
		t.Errorf("title = %q; the refresh rewrote a retitled book's title", books[0].Title)
	}
}
