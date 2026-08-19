package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// These two tests answer the review question on #2041 end to end: does setting
// metadata.primary_provider=hardcover on a library whose rows all carry
// OpenLibrary foreign IDs duplicate that library on the next sync?
//
// The answer has two halves, and they are separated deliberately because they
// have different answers.

// olSeed is one seeded row as an OpenLibrary sync would have left it.
type olSeed struct {
	foreignID string
	title     string
}

func seedOpenLibraryLibrary(t *testing.T, authorRepo *db.AuthorRepo, bookRepo *db.BookRepo, authorForeignID string, seeds []olSeed) *models.Author {
	t.Helper()
	ctx := context.Background()

	author := &models.Author{
		ForeignID:        authorForeignID,
		Name:             "Clive Cussler",
		SortName:         "Cussler, Clive",
		MetadataProvider: "openlibrary",
		Monitored:        false,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	for _, s := range seeds {
		b := &models.Book{
			ForeignID: s.foreignID, AuthorID: author.ID,
			Title: s.title, SortTitle: s.title,
			Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary", Monitored: false,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	return author
}

// TestPrimaryFlipToHardcover_DoesNotDuplicateOpenLibraryLibrary is the test the
// review asked for: seed an OpenLibrary-sourced library, flip the primary to
// Hardcover, sync, assert no duplicates.
//
// It passes because the flip does not re-source the author. The catalogue
// provider is chosen from the AUTHOR's foreign-ID prefix
// (metadata.providerForForeignID), not from which provider is primary, and
// main.go keeps OpenLibrary wired as an enricher when Hardcover is promoted —
// so an "OL…" author is still answered by OpenLibrary and every work comes
// back with the foreign ID the library already stores. Nothing reaches the
// title-dedup fallback at all.
//
// Note this uses FetchAuthorBooks, not RefreshAuthorBooks. The refresh path
// gates row CREATION on the author's monitoring policy (#1815), so an
// unmonitored author cannot grow no matter what the provider returns, and a
// duplicate-freedom assertion there would pass vacuously. FetchAuthorBooks
// permits creation, which makes this the strictly stronger claim: rows could
// have been created, and none were.
func TestPrimaryFlipToHardcover_DoesNotDuplicateOpenLibraryLibrary(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	seeds := []olSeed{
		{"OL911W", "Raise the Titanic"},
		{"OL912W", "Poseidon's Arrow"},
		{"OL913W", "Journey of the Pharaohs: Numa Files #17"},
	}
	author := seedOpenLibraryLibrary(t, authorRepo, bookRepo, "OL26320A", seeds)

	// The OpenLibrary provider still returns the same catalogue it always did.
	olWorks := make([]models.Book, 0, len(seeds))
	for _, s := range seeds {
		olWorks = append(olWorks, models.Book{
			ForeignID: s.foreignID, Title: s.title, SortTitle: s.title,
			Status: models.BookStatusWanted, Genres: []string{},
			MetadataProvider: "openlibrary",
		})
	}
	ol := &stubMetaProvider{name: "openlibrary", works: olWorks}

	// Hardcover holds the same works under its own IDs, and with the
	// punctuation it happens to use. If the flip re-sourced this author, these
	// are the records that would arrive — and the last one would not dedup.
	hc := &stubMetaProvider{
		name: "hardcover",
		works: []models.Book{
			{ForeignID: "hc:1", Title: "Raise the Titanic", SortTitle: "raise the titanic", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
			{ForeignID: "hc:2", Title: "Poseidon's Arrow", SortTitle: "poseidon's arrow", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
			{ForeignID: "hc:3", Title: "Journey of the Pharaohs Numa Files #17", SortTitle: "journey of the pharaohs numa files #17", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
		},
	}

	// Post-flip wiring, mirroring cmd/bindery/main.go: hardcover primary, every
	// other provider demoted to enricher.
	agg := metadata.NewAggregator(hc, ol)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != len(seeds) {
		for _, b := range books {
			t.Logf("  %-45q %s", b.Title, b.ForeignID)
		}
		t.Fatalf("flipping the primary to Hardcover changed the library from %d rows to %d", len(seeds), len(books))
	}
	for _, b := range books {
		if b.MetadataProvider == "hardcover" {
			t.Errorf("row %q was re-sourced to hardcover (%s); an OpenLibrary-linked "+
				"author must keep answering from OpenLibrary after the flip", b.Title, b.ForeignID)
		}
	}
}

// TestRelinkedAuthorDedupsByTitle covers the case that IS exposed to title
// dedup, including the punctuation divergence between the two providers.
//
// A global primary flip leaves author.ForeignID alone. The per-author relink
// endpoint (POST /api/v1/author/{id}/relink-upstream) rewrites it, so the next
// sync legitimately fetches from Hardcover and every work arrives with an hc:
// id that matches no existing row by foreign ID. Reconciliation then rests
// entirely on indexer.CanonicalDedupKey.
//
// This test originally asserted that the two punctuation-divergent pairs
// duplicated, because CanonicalDedupKey folded neither apostrophes nor a
// subtitle expressed without its colon (#2042). That fix has since landed in
// #2043, so all three pairs now collapse onto the seeded row and the relink
// path no longer needs a caveat about punctuation.
func TestRelinkedAuthorDedupsByTitle(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	seeds := []olSeed{
		// Identical punctuation on both sides.
		{"OL911W", "Raise the Titanic"},
		// Hardcover drops the apostrophe; folded since #2043.
		{"OL912W", "Poseidon's Arrow"},
		// Hardcover drops the subtitle colon; folded since #2043.
		{"OL913W", "Journey of the Pharaohs: Numa Files #17"},
	}
	// The relink has already happened: the author now carries a Hardcover ID
	// while every book row still carries its OpenLibrary one.
	author := seedOpenLibraryLibrary(t, authorRepo, bookRepo, "hc:4321", seeds)

	hc := &stubMetaProvider{
		name: "hardcover",
		works: []models.Book{
			{ForeignID: "hc:1", Title: "Raise the Titanic", SortTitle: "raise the titanic", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
			{ForeignID: "hc:2", Title: "Poseidons Arrow", SortTitle: "poseidons arrow", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
			{ForeignID: "hc:3", Title: "Journey of the Pharaohs Numa Files #17", SortTitle: "journey of the pharaohs numa files #17", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"},
		},
	}
	ol := &stubMetaProvider{name: "openlibrary"}

	agg := metadata.NewAggregator(hc, ol)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, nil, profileRepo, nil)
	h.FetchAuthorBooks(author, false, "")

	books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}

	byTitle := map[string]int{}
	for _, b := range books {
		byTitle[b.Title]++
	}

	// Every incoming work must land on its seeded row: the identical title by
	// plain equality, and the two punctuation-divergent ones through the
	// apostrophe folding and colon handling added in #2043.
	deduped := [][2]string{
		{"Raise the Titanic", "Raise the Titanic"},
		{"Poseidon's Arrow", "Poseidons Arrow"},
		{"Journey of the Pharaohs: Numa Files #17", "Journey of the Pharaohs Numa Files #17"},
	}
	for _, pair := range deduped {
		seeded, incoming := pair[0], pair[1]
		if byTitle[seeded] != 1 {
			t.Errorf("seeded title %q appears %d times, want 1", seeded, byTitle[seeded])
		}
		if seeded != incoming && byTitle[incoming] != 0 {
			t.Errorf("%q failed to dedup onto %q: the incoming spelling kept its own row", incoming, seeded)
		}
	}

	if len(books) != 3 {
		for _, b := range books {
			t.Logf("  %-45q %s", b.Title, b.ForeignID)
		}
		t.Fatalf("expected 3 rows (every incoming work deduped onto its seed), got %d", len(books))
	}
}
