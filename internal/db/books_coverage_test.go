package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// helper: create an author and return it.
func mkAuthor(t *testing.T, repo *AuthorRepo, ctx context.Context, fid string) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: fid, Name: "A " + fid, SortName: "A " + fid, MetadataProvider: "openlibrary", Monitored: true}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	return a
}

// helper: create a book and return it.
func mkBook(t *testing.T, repo *BookRepo, ctx context.Context, authorID int64, fid, title, status string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title,
		Status: status, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBookRepo_ExcludedFlagAndListVariants(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-EX-A")
	kept := mkBook(t, bookRepo, ctx, a.ID, "OL-EX-K", "Kept", "wanted")
	hidden := mkBook(t, bookRepo, ctx, a.ID, "OL-EX-H", "Hidden", "wanted")

	// Exclude one book.
	if err := bookRepo.SetExcluded(ctx, hidden.ID, true); err != nil {
		t.Fatalf("SetExcluded: %v", err)
	}

	// Confirm Excluded round-trips.
	got, _ := bookRepo.GetByID(ctx, hidden.ID)
	if !got.Excluded {
		t.Error("expected Excluded=true after SetExcluded")
	}

	// List() hides excluded rows; ListIncludingExcluded() shows them.
	visible, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != kept.ID {
		t.Errorf("List should hide excluded, got %+v", visible)
	}

	all, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("ListIncludingExcluded: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListIncludingExcluded: want 2, got %d", len(all))
	}

	// ListByAuthor / IncludingExcluded
	byAuthor, _ := bookRepo.ListByAuthor(ctx, a.ID)
	if len(byAuthor) != 1 {
		t.Errorf("ListByAuthor: want 1, got %d", len(byAuthor))
	}
	byAuthorAll, _ := bookRepo.ListByAuthorIncludingExcluded(ctx, a.ID)
	if len(byAuthorAll) != 2 {
		t.Errorf("ListByAuthorIncludingExcluded: want 2, got %d", len(byAuthorAll))
	}

	// ListByStatus / IncludingExcluded
	byStatus, _ := bookRepo.ListByStatus(ctx, "wanted")
	if len(byStatus) != 1 {
		t.Errorf("ListByStatus: want 1, got %d", len(byStatus))
	}
	byStatusAll, _ := bookRepo.ListByStatusIncludingExcluded(ctx, "wanted")
	if len(byStatusAll) != 2 {
		t.Errorf("ListByStatusIncludingExcluded: want 2, got %d", len(byStatusAll))
	}

	// Unexclude.
	if err := bookRepo.SetExcluded(ctx, hidden.ID, false); err != nil {
		t.Fatalf("SetExcluded(false): %v", err)
	}
	got, _ = bookRepo.GetByID(ctx, hidden.ID)
	if got.Excluded {
		t.Error("expected Excluded=false after clearing")
	}
	visible, _ = bookRepo.List(ctx)
	if len(visible) != 2 {
		t.Errorf("after unexclude, List: want 2, got %d", len(visible))
	}
}

func TestBookRepo_CalibreIDRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-CL-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-CL-B", "Calibre Book", "wanted")

	// Before setting, GetByCalibreID returns nil.
	missing, err := bookRepo.GetByCalibreID(ctx, 12345)
	if err != nil {
		t.Fatalf("GetByCalibreID missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for unset calibre id")
	}

	// Set and look up.
	if err := bookRepo.SetCalibreID(ctx, b.ID, 12345); err != nil {
		t.Fatalf("SetCalibreID: %v", err)
	}
	got, err := bookRepo.GetByCalibreID(ctx, 12345)
	if err != nil {
		t.Fatalf("GetByCalibreID: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("unexpected book: %+v", got)
	}
	if got.CalibreID == nil || *got.CalibreID != 12345 {
		t.Errorf("CalibreID not populated: %v", got.CalibreID)
	}
}

func TestBookRepo_FindByAuthorAndTitleCaseInsensitive(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-FT-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-FT-B", "The Great Novel", "wanted")

	// Exact match.
	got, err := bookRepo.FindByAuthorAndTitle(ctx, a.ID, "The Great Novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle exact: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("exact match failed: %+v", got)
	}

	// Case-insensitive match.
	got, err = bookRepo.FindByAuthorAndTitle(ctx, a.ID, "the great novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle lower: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("case-insensitive match failed: %+v", got)
	}

	// Wrong author — no match.
	got, err = bookRepo.FindByAuthorAndTitle(ctx, 999, "The Great Novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle other author: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong author, got %+v", got)
	}

	// No match on title.
	got, _ = bookRepo.FindByAuthorAndTitle(ctx, a.ID, "Unrelated")
	if got != nil {
		t.Error("expected nil for unrelated title")
	}
}
