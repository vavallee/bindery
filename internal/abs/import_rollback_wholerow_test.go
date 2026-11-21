package abs

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_RollbackRestoresWholeBookRow compares the entire book row, not
// a list of fields, before the import and after the rollback. It complements
// TestImporter_RollbackRestoresExistingBookMetadata, which asserts named
// fields only and so cannot notice a column the snapshot forgot.
func TestImporter_RollbackRestoresWholeBookRow(t *testing.T) {
	t.Parallel()

	importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-AUTHOR", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	refreshed := time.Date(2020, 6, 7, 8, 9, 10, 0, time.UTC)
	book := &models.Book{
		ForeignID:             "OL-LOCAL-BOOK",
		AuthorID:              author.ID,
		Title:                 "Local Title",
		SortTitle:             "Local Title",
		OriginalTitle:         "Original Local Title",
		Description:           "Local description.",
		ImageURL:              "https://covers.example.com/local.jpg",
		ReleaseDate:           mustDate(t, "1999-01-02"),
		Genres:                []string{"local", "sci-fi"},
		AverageRating:         4.2,
		RatingsCount:          17,
		Monitored:             false,
		Status:                models.BookStatusSkipped,
		Language:              "fre",
		MediaType:             models.MediaTypeEbook,
		Narrator:              "Local Narrator",
		DurationSeconds:       12,
		ASIN:                  "LOCALASIN",
		MetadataProvider:      "openlibrary",
		LastMetadataRefreshAt: &refreshed,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("Create book: %v", err)
	}
	item := sampleABSItem()
	item.Title = "ABS Title"
	item.Description = "ABS description."
	item.Language = "eng"
	item.Genres = []string{"imported"}
	item.ASIN = "ABSASIN"
	if err := provenanceRepo.Upsert(ctx, &models.ABSProvenance{
		SourceID: DefaultSourceID, LibraryID: item.LibraryID, EntityType: entityTypeBook,
		ExternalID: item.ItemID, LocalID: book.ID, ItemID: item.ItemID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	original, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil || original == nil {
		t.Fatalf("GetByID: %v", err)
	}

	runID := runSingleABSImport(t, importer, item)
	imported, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil || imported.Title != "ABS Title" || imported.Description != "ABS description." {
		t.Fatalf("import did not update the row: %+v err=%v", imported, err)
	}
	if _, err := importer.Rollback(ctx, runID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	restored, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil || restored == nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	// UpdatedAt and UpdatedAtRaw move on every write; every other column must match.
	restored.UpdatedAt, original.UpdatedAt = time.Time{}, time.Time{}
	restored.UpdatedAtRaw, original.UpdatedAtRaw = "", ""
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("rollback did not restore the row\n got %+v\nwant %+v", restored, original)
	}
}
