package calibre

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_CrossSourceBindsByDedupKey is the #940 regression for the
// Calibre side (the mirror of the ABS-side test). A book already filed by ABS
// under one title form must be LINKED via Path 3 (author + canonical dedup_key)
// when Calibre imports the same work under a differing-but-equivalent title,
// not duplicated. Pre-fix, Path 3 matched raw LOWER(title) and created a 2nd
// row for every subtitle/case/umlaut variant.
func TestImporter_CrossSourceBindsByDedupKey(t *testing.T) {
	cases := []struct {
		name         string
		absTitle     string // title ABS persisted
		calibreTitle string // title Calibre presents for the same work
	}{
		{"calibre drops subtitle abs keeps it", "Mistborn: The Final Empire", "Mistborn"},
		{"calibre keeps subtitle abs dropped it", "Mistborn", "Mistborn: The Final Empire"},
		{"case differs", "Elantris", "ELANTRIS"},
		{"umlaut form differs", "Die Strasse", "Die Straße"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)
			ctx := context.Background()

			// Seed an author + an ABS-style book (plain Create -> dedup_key set).
			author := &models.Author{ForeignID: "abs:author:1", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", Monitored: true, MetadataProvider: "audiobookshelf"}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			absBook := &models.Book{
				ForeignID: "abs:book:1", AuthorID: author.ID, Title: tc.absTitle, SortTitle: tc.absTitle,
				Status: models.BookStatusImported, Genres: []string{}, MetadataProvider: "audiobookshelf",
				MediaType: models.MediaTypeAudiobook, Monitored: true,
			}
			if err := bookRepo.Create(ctx, absBook); err != nil {
				t.Fatal(err)
			}

			fr.books = []CalibreBook{sampleCalibreBook(1, tc.calibreTitle, "Brandon Sanderson")}
			stats, err := imp.Run(ctx, "/lib")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if stats.BooksAdded != 0 {
				t.Fatalf("Calibre import created a duplicate (BooksAdded=%d) for abs=%q calibre=%q", stats.BooksAdded, tc.absTitle, tc.calibreTitle)
			}

			books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				t.Fatalf("expected 1 book after cross-source bind, got %d", len(books))
			}
			// The single surviving row must now carry the Calibre id (Path 3
			// links by SetCalibreID).
			linked, err := bookRepo.GetByCalibreID(ctx, 1)
			if err != nil || linked == nil {
				t.Fatalf("book not linked to calibre_id: %v / %v", err, linked)
			}
			if linked.ID != absBook.ID {
				t.Fatalf("Calibre linked to a new row (%d) instead of the ABS row (%d)", linked.ID, absBook.ID)
			}
		})
	}
}
