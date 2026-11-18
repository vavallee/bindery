package calibre

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_RollbackRestoresUpdatedBookRow imports over a book row that
// already exists, rolls the run back, and requires the row to read as it did
// before the import. The row carries Description, Genres, AverageRating and
// RatingsCount, which the Calibre importer does not write: they must be
// identical at every step, so the snapshot does not need to capture them.
func TestImporter_RollbackRestoresUpdatedBookRow(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, runsRepo, _, _ := newRollbackFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-A", Name: "Alice Author", SortName: "Author, Alice", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	released := time.Date(1999, 3, 4, 0, 0, 0, 0, time.UTC)
	seed := &models.Book{
		ForeignID:        "OL-B",
		AuthorID:         author.ID,
		Title:            "Old Title",
		SortTitle:        "Old Title",
		Description:      "Written by the metadata provider.",
		Genres:           []string{"Fantasy", "Classics"},
		AverageRating:    4.25,
		RatingsCount:     1234,
		ReleaseDate:      &released,
		Status:           models.BookStatusWanted,
		MetadataProvider: "openlibrary",
		MediaType:        models.MediaTypeEbook,
		Monitored:        true,
		AnyEditionOK:     true,
	}
	if err := bookRepo.Create(ctx, seed); err != nil {
		t.Fatalf("seed book: %v", err)
	}
	if err := bookRepo.SetCalibreID(ctx, seed.ID, 42); err != nil {
		t.Fatal(err)
	}
	original, err := bookRepo.GetByID(ctx, seed.ID)
	if err != nil || original == nil {
		t.Fatalf("GetByID: %v", err)
	}

	cb := sampleCalibreBook(42, "New Title", "Alice Author")
	cb.Language = "eng"
	fr.books = []CalibreBook{cb}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	imported, err := bookRepo.GetByID(ctx, seed.ID)
	if err != nil || imported == nil {
		t.Fatalf("GetByID after import: %v", err)
	}
	if imported.Title != "New Title" || imported.FilePath == "" || imported.Language != "eng" {
		t.Fatalf("import did not update the row: title=%q path=%q language=%q", imported.Title, imported.FilePath, imported.Language)
	}
	if imported.Description != original.Description || !reflect.DeepEqual(imported.Genres, original.Genres) ||
		imported.AverageRating != original.AverageRating || imported.RatingsCount != original.RatingsCount {
		t.Fatalf("import wrote a field the Calibre snapshot does not capture: %+v", imported)
	}

	runs, _ := runsRepo.ListRecent(ctx, 1)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if _, err := imp.Rollback(ctx, runs[0].ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	restored, err := bookRepo.GetByID(ctx, seed.ID)
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
