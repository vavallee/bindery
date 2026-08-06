package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// runMultiABSImport enumerates the given items in order through a single live
// import run, so a row created for an earlier item is visible to the dedup match
// of a later one (the exact ordering that produced the collapse in #1785).
func runMultiABSImport(t *testing.T, importer *Importer, items ...NormalizedLibraryItem) {
	t.Helper()
	importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for i := range items {
			if err := fn(ctx, items[i]); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}
	if _, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func seriesVolumeItem(itemID, title, seriesName, sequence string) NormalizedLibraryItem {
	item := sampleABSItem()
	item.ItemID = itemID
	item.Title = title
	item.ASIN = ""
	item.Authors = []NormalizedAuthor{{ID: "author-tao-wong", Name: "Tao Wong"}}
	item.Series = []NormalizedSeries{{ID: "series-a-thousand-li", Name: seriesName, Sequence: sequence}}
	item.AudioFiles = nil
	item.EbookPath = ""
	item.EbookINO = ""
	return item
}

// TestImporter_SeriesVolumesWithSharedBaseTitleStayDistinct is the #1785
// regression. indexer.CanonicalDedupKey strips a ": subtitle" tail, so every
// "A Thousand Li: <volume>" collapses to the key "a thousand li". Before the
// fix, importing volume 2 linked it onto volume 1 (and volume 3+ hit ambiguity
// → review), so a 902-item library created only ~824 books. With series
// awareness, distinct volumes (same series, different sequence) each create a
// row.
func TestImporter_SeriesVolumesWithSharedBaseTitleStayDistinct(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-TW", Name: "Tao Wong", SortName: "Wong, Tao", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	runMultiABSImport(t, importer,
		seriesVolumeItem("li-atl-1", "A Thousand Li: The First Step", "A Thousand Li", "1"),
		seriesVolumeItem("li-atl-2", "A Thousand Li: The First War", "A Thousand Li", "2"),
		seriesVolumeItem("li-atl-3", "A Thousand Li: The Second Expedition", "A Thousand Li", "3"),
	)

	if got := countBooksForAuthor(t, bookRepo, author.ID); got != 3 {
		t.Fatalf("expected 3 distinct volumes, got %d (series volumes collapsed onto one dedup key)", got)
	}
}

// TestImporter_SameVolumeDifferentFormatStillLinks guards against over-splitting:
// the audiobook edition of a volume drops the subtitle ("A Thousand Li") but
// carries the same series sequence as the ebook, so it must still LINK onto the
// existing row (#940 behavior), not create a second one.
func TestImporter_SameVolumeDifferentFormatStillLinks(t *testing.T) {
	importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-TW", Name: "Tao Wong", SortName: "Wong, Tao", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	ebook := seriesVolumeItem("li-atl-1-ebook", "A Thousand Li: The First Step", "A Thousand Li", "1")
	ebook.MediaType = "book"
	audiobook := seriesVolumeItem("li-atl-1-audio", "A Thousand Li", "A Thousand Li", "1")

	runMultiABSImport(t, importer, ebook, audiobook)

	if got := countBooksForAuthor(t, bookRepo, author.ID); got != 1 {
		t.Fatalf("expected same-sequence editions to link into 1 book, got %d", got)
	}
}
