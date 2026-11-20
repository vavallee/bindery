package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// sameTitleTwoFormats is one work held as two library items, an ebook and an
// audiobook, the way BookOrbit lists it.
func sameTitleTwoFormats() []NormalizedLibraryItem {
	ebook := NormalizedLibraryItem{
		ItemID: "1", LibraryID: "othersource-1", Title: "Pride and Prejudice",
		PublishedYear: "2023",
		Authors:       []NormalizedAuthor{{ID: "a-1", Name: "Jane Austen"}},
		EbookPath:     "/lib/Jane Austen/Pride and Prejudice/pride.epub",
		EbookINO:      "ino-epub",
	}
	audio := NormalizedLibraryItem{
		ItemID: "8", LibraryID: "othersource-1", Title: "Pride and Prejudice",
		PublishedYear: "1813",
		Authors:       []NormalizedAuthor{{ID: "a-1", Name: "Jane Austen"}},
		AudioFiles:    []NormalizedAudioFile{{INO: "ino-m4b", Path: "/lib/Jane Austen/Pride and Prejudice/pride.m4b"}},
	}
	return []NormalizedLibraryItem{ebook, audio}
}

// The preview has to predict what the real import does. A second item that
// holds the same work as the first links to the book the first one made, so a
// preview that cannot see that book counts it as a second creation.
func TestDryRunPredictsSameTitleTwoFormatsLikeRealRun(t *testing.T) {
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	importer.WithEnumerator(itemsEnumerator(sameTitleTwoFormats()...))

	preview, err := importer.Run(context.Background(), otherSourceConfig(true))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	all, err := bookRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("dry run wrote %d books", len(all))
	}

	real, err := importer.Run(context.Background(), otherSourceConfig(false))
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if real.BooksCreated != 1 || real.BooksLinked != 1 {
		t.Fatalf("real run created %d linked %d, want 1 and 1", real.BooksCreated, real.BooksLinked)
	}
	if preview.BooksCreated != real.BooksCreated || preview.BooksLinked != real.BooksLinked ||
		preview.BooksUpdated != real.BooksUpdated || preview.EditionsAdded != real.EditionsAdded {
		t.Errorf("preview created/linked/updated/editions = %d/%d/%d/%d, real = %d/%d/%d/%d",
			preview.BooksCreated, preview.BooksLinked, preview.BooksUpdated, preview.EditionsAdded,
			real.BooksCreated, real.BooksLinked, real.BooksUpdated, real.EditionsAdded)
	}
	books, _ := bookRepo.List(context.Background())
	if len(books) != 1 || books[0].MediaType != models.MediaTypeBoth {
		t.Errorf("real run left %d books, want 1 of media type both", len(books))
	}
}

// Same title under two different authors is two books, in the preview too.
func TestDryRunKeepsSameTitleByDifferentAuthorsApart(t *testing.T) {
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	items := sameTitleTwoFormats()
	items[1].Authors = []NormalizedAuthor{{ID: "a-2", Name: "Someone Else"}}
	importer.WithEnumerator(itemsEnumerator(items...))
	preview, err := importer.Run(context.Background(), otherSourceConfig(true))
	if err != nil {
		t.Fatal(err)
	}
	if preview.BooksCreated != 2 || preview.BooksLinked != 0 {
		t.Errorf("preview created %d linked %d, want 2 and 0", preview.BooksCreated, preview.BooksLinked)
	}
}

// Two volumes of one series that share a title stay two books in the preview,
// as they do in the real run.
func TestDryRunKeepsSameTitleSeriesVolumesApart(t *testing.T) {
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	items := sameTitleTwoFormats()
	items[0].Series = []NormalizedSeries{{ID: "s", Name: "Saga", Sequence: "1"}}
	items[1].Series = []NormalizedSeries{{ID: "s", Name: "Saga", Sequence: "2"}}
	importer.WithEnumerator(itemsEnumerator(items...))

	preview, err := importer.Run(context.Background(), otherSourceConfig(true))
	if err != nil {
		t.Fatal(err)
	}
	real, err := importer.Run(context.Background(), otherSourceConfig(false))
	if err != nil {
		t.Fatal(err)
	}
	if real.BooksCreated != 2 || preview.BooksCreated != real.BooksCreated || preview.BooksLinked != real.BooksLinked {
		t.Errorf("preview created/linked = %d/%d, real = %d/%d, want 2/0 both",
			preview.BooksCreated, preview.BooksLinked, real.BooksCreated, real.BooksLinked)
	}
}

func assertPreviewMatchesReal(t *testing.T, items []NormalizedLibraryItem) (*ImportStats, *ImportStats) {
	t.Helper()
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	importer.WithEnumerator(itemsEnumerator(items...))
	preview, err := importer.Run(context.Background(), otherSourceConfig(true))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	real, err := importer.Run(context.Background(), otherSourceConfig(false))
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	type counts struct{ authorsCreated, authorsLinked, booksCreated, booksLinked, seriesCreated, seriesLinked, editions int }
	got := counts{preview.AuthorsCreated, preview.AuthorsLinked, preview.BooksCreated, preview.BooksLinked, preview.SeriesCreated, preview.SeriesLinked, preview.EditionsAdded}
	want := counts{real.AuthorsCreated, real.AuthorsLinked, real.BooksCreated, real.BooksLinked, real.SeriesCreated, real.SeriesLinked, real.EditionsAdded}
	if got != want {
		t.Errorf("preview %+v, real %+v", got, want)
	}
	return preview, real
}

// Both formats of one title in one series: the second item joins the book the
// first made, so the series gains one member, not two.
func TestDryRunCountsSeriesMembershipOfSharedBookOnce(t *testing.T) {
	items := sameTitleTwoFormats()
	for idx := range items {
		items[idx].Series = []NormalizedSeries{{ID: "s-pp", Name: "Bennet Family", Sequence: "1"}}
	}
	_, real := assertPreviewMatchesReal(t, items)
	if real.BooksCreated != 1 || real.SeriesCreated != 1 || real.SeriesLinked != 0 {
		t.Fatalf("real run = %+v, want one book in one new series", real)
	}
}

// The author spelled two ways: the real run's matcher folds the second
// spelling onto the author the first created, and the preview has to as well.
func TestDryRunFoldsAuthorSpellingVariant(t *testing.T) {
	items := sameTitleTwoFormats()
	items[1].Authors = []NormalizedAuthor{{ID: "a-2", Name: "Austen, Jane"}}
	_, real := assertPreviewMatchesReal(t, items)
	if real.AuthorsCreated != 1 || real.BooksCreated != 1 || real.BooksLinked != 1 {
		t.Fatalf("real run = %+v, want one author and one book", real)
	}
}
