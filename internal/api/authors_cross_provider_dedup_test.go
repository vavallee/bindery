package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestFetchAuthorBooks_CrossProviderWorkDoesNotMintASecondRow reproduces #1705.
//
// The reported sequence: a Hardcover-linked series fills its books, the user
// runs Find better metadata on the author and picks their OpenLibrary entry,
// then refreshes. The refresh fetches OpenLibrary works, whose ids no existing
// row has ever held, and used to create a parallel row for a volume already in
// the library. The reporter was left with two rows for volume 1 of The Mimosa
// Confessions and no way to merge them that did not cost the series link.
//
// The link between the two is HardcoverForeignID: mergeAuthorWorks joins the
// providers' catalogues in memory before any of this, so the OpenLibrary work
// arrives carrying the Hardcover id of the same book.
func TestFetchAuthorBooks_CrossProviderWorkDoesNotMintASecondRow(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	// The author, now relinked to OpenLibrary.
	author := &models.Author{
		ForeignID: "OL500A", Name: "Mimosa Author", SortName: "Author, Mimosa",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// The book as the series fill created it: from Hardcover.
	existing := &models.Book{
		ForeignID: "hc:mimosa-1", AuthorID: author.ID,
		Title: "The Mimosa Confessions", SortTitle: "mimosa confessions",
		Genres: []string{}, Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover",
		Language: "eng",
	}
	if err := bookRepo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// The refresh returns the same volume as an OpenLibrary work. The title
	// differs, so the canonical-title fallback cannot save this: the id link is
	// the only thing connecting them.
	works := []models.Book{{
		ForeignID: "OL500W", Title: "Mimosa Confessions, The", SortTitle: "mimosa confessions the",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", HardcoverForeignID: "hc:mimosa-1",
	}}
	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider),
		settingsRepo, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		for _, b := range got {
			t.Logf("book %d: %q (%s)", b.ID, b.Title, b.ForeignID)
		}
		t.Fatalf("author has %d books, want 1; the same volume was created twice", len(got))
	}
	if got[0].ID != existing.ID {
		t.Errorf("surviving row is %d, want the original %d", got[0].ID, existing.ID)
	}

	// The OpenLibrary id is now on record, so the next refresh matches exactly
	// rather than depending on the ids lining up again.
	resolved, err := bookRepo.GetByAnyForeignID(ctx, "OL500W")
	if err != nil {
		t.Fatalf("lookup by OL id: %v", err)
	}
	if resolved == nil || resolved.ID != existing.ID {
		t.Errorf("the OpenLibrary id was not recorded against the matched row: %+v", resolved)
	}
}

// TestFetchAuthorBooks_UnrelatedWorkStillCreatesARow is the guard on the other
// side: widening the lookup must not start collapsing genuinely distinct books,
// which is the failure #1704 had to fix in the opposite direction.
func TestFetchAuthorBooks_UnrelatedWorkStillCreatesARow(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	author := &models.Author{
		ForeignID: "OL501A", Name: "Series Author", SortName: "Author, Series",
		MetadataProvider: "openlibrary", Monitored: false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	volume1 := &models.Book{
		ForeignID: "hc:vol-1", AuthorID: author.ID,
		Title: "Volume One", SortTitle: "volume one",
		Genres: []string{}, Status: models.BookStatusWanted,
		MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover", Language: "eng",
	}
	if err := bookRepo.Create(ctx, volume1); err != nil {
		t.Fatal(err)
	}

	// A different volume, carrying its own Hardcover link.
	works := []models.Book{{
		ForeignID: "OL502W", Title: "Volume Two", SortTitle: "volume two",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		MetadataProvider: "openlibrary", HardcoverForeignID: "hc:vol-2",
	}}
	provider := &stubMetaProvider{works: works}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider),
		settingsRepo, profileRepo, nil)
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	got, err := bookRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("author has %d books, want 2; a distinct volume was collapsed", len(got))
	}
}
