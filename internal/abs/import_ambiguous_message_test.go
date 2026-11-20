package abs

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func ambiguityFixture(t *testing.T, bookForeignID, bookProvider string, works []models.Book) (*Importer, *models.Author, *models.Book) {
	t.Helper()
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-JA", Name: "Jane Austen", SortName: "Austen, Jane", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: bookForeignID, AuthorID: author.ID, Title: "Pride and Prejudice", SortTitle: "Pride and Prejudice",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: bookProvider,
		MediaType: models.MediaTypeEbook, Monitored: true,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	stub := &stubABSMetadataProvider{
		works: map[string][]models.Book{"OL-JA": works},
		books: map[string]*models.Book{},
	}
	for idx := range works {
		w := works[idx]
		stub.books[w.ForeignID] = &w
	}
	importer.WithMetadata(metadata.NewAggregator(stub))
	return importer, author, book
}

func prideWorks(foreignIDs ...string) []models.Book {
	var out []models.Book
	for _, id := range foreignIDs {
		out = append(out, models.Book{ForeignID: id, Title: "Pride and Prejudice", MetadataProvider: "openlibrary"})
	}
	return out
}

func enrichMessages(t *testing.T, importer *Importer, author *models.Author, book *models.Book) string {
	t.Helper()
	item := NormalizedLibraryItem{ItemID: "1", LibraryID: "othersource-1", Title: "Pride and Prejudice", Authors: []NormalizedAuthor{{ID: "a", Name: "Jane Austen"}}}
	res, err := importer.enrichBook(context.Background(), ImportConfig{SourceID: "othersource"}, item, author, book)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(res.Messages, "; ")
}

// A book that is still on its import placeholder and has several equally good
// provider works is a real ambiguity, and the user is told so in plain words.
func TestEnrichBookReportsGenuineAmbiguity(t *testing.T) {
	importer, author, book := ambiguityFixture(t, "abs:book:othersource-1:1", providerAudiobookshelf, prideWorks("OL-W1", "OL-W2"))
	got := enrichMessages(t, importer, author, book)
	if !strings.Contains(got, "not linked to a metadata provider") || !strings.Contains(got, "none was picked") {
		t.Fatalf("message = %q, want the ambiguity reported", got)
	}
	if strings.Contains(got, "relink skipped") {
		t.Fatalf("message = %q still uses the old wording", got)
	}
}

// A book that already has its provider identity is not waiting for one, so the
// same several works say nothing, on the first import and on every rerun.
func TestEnrichBookSilentWhenAlreadyBound(t *testing.T) {
	importer, author, book := ambiguityFixture(t, "OL-W1", "openlibrary", prideWorks("OL-W1", "OL-W2"))
	for run := 1; run <= 2; run++ {
		if got := enrichMessages(t, importer, author, book); got != "" {
			t.Fatalf("run %d message = %q, want none", run, got)
		}
	}
}

// The ordinary path: one work matches, the book is relinked once, and the
// rerun has nothing to say.
func TestEnrichBookNormalImportAndRerunAreSilentAboutAmbiguity(t *testing.T) {
	importer, author, book := ambiguityFixture(t, "abs:book:othersource-1:1", providerAudiobookshelf, prideWorks("OL-W1"))
	first := enrichMessages(t, importer, author, book)
	if strings.Contains(first, "ambiguous") || strings.Contains(first, "not linked") || strings.Contains(first, "skipped") {
		t.Fatalf("first import message = %q", first)
	}
	if got := enrichMessages(t, importer, author, book); got != "" {
		t.Fatalf("rerun message = %q, want none", got)
	}
}

// The same rule for authors: an ambiguous name lookup is reported for an
// author with no provider identity and is silent for one that has it.
func TestEnrichAuthorAmbiguityOnlyForUnlinkedAuthor(t *testing.T) {
	two := []models.Author{
		{ForeignID: "OL-A1", Name: "Jon Smith", MetadataProvider: "openlibrary"},
		{ForeignID: "OL-A2", Name: "John Smyth", MetadataProvider: "openlibrary"},
	}
	for _, tc := range []struct {
		name      string
		foreignID string
		provider  string
		want      bool
	}{
		{"placeholder author", "abs:author:x", providerAudiobookshelf, true},
		{"linked author", "OL-JA", "openlibrary", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importer, authorRepo, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			ctx := context.Background()
			author := &models.Author{ForeignID: tc.foreignID, Name: "John Smith", SortName: "Smith, John", Monitored: true, MetadataProvider: tc.provider}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			importer.WithMetadata(metadata.NewAggregator(&stubABSMetadataProvider{searchAuthors: two}))
			item := NormalizedLibraryItem{Authors: []NormalizedAuthor{{ID: "a", Name: "John Smith"}}}
			res, err := importer.enrichAuthor(ctx, ImportConfig{SourceID: "othersource"}, item, author, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(res.Messages, "; ")
			if tc.want != strings.Contains(got, "none was picked") {
				t.Fatalf("message = %q, want reported = %v", got, tc.want)
			}
		})
	}
}

// A book added by hand (#3840) is left on its manual identity: an import that
// matches it by title must not relink it to the one provider work that fits.
func TestEnrichBookLeavesManualBookOnItsOwnIdentity(t *testing.T) {
	importer, author, book := ambiguityFixture(t, "manual:book:abc", models.ManualMetadataProvider, prideWorks("OL-W1"))
	if got := enrichMessages(t, importer, author, book); got != "" {
		t.Fatalf("message = %q, want none", got)
	}
	if book.ForeignID != "manual:book:abc" || book.MetadataProvider != models.ManualMetadataProvider {
		t.Fatalf("book = %q / %q, want it left on its manual identity", book.ForeignID, book.MetadataProvider)
	}
}

// The title a manual book is created with is locked, so the field merge an ABS
// import runs on a title match keeps what the user typed.
func TestMergeBookFieldsKeepsLockedManualTitle(t *testing.T) {
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	book := &models.Book{Title: "The Last Church", ForeignID: "manual:book:abc", LockedFields: []string{models.BookFieldTitle}}
	importer.mergeBookFields(book, 1, NormalizedLibraryItem{Title: "The last church (unabridged)"}, false)
	if book.Title != "The Last Church" {
		t.Fatalf("title = %q, want the locked title kept", book.Title)
	}
}
