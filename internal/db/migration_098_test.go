package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate098_AddsPriorBookStatusColumn covers migration 098 adding
// prior_book_status to unmatched_units (#2885).
func TestMigrate098_AddsPriorBookStatusColumn(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "ol:a", Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:b", AuthorID: author.ID, Title: "Ancillary Justice",
		Status: models.BookStatusSkipped, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	repo := NewUnmatchedUnitRepo(database)
	unit := scanUnit("/lib/A/test.epub")
	if _, err := repo.ReconcileScan(ctx, []UnmatchedUnitScan{unit}, ReconcileScanOptions{}); err != nil {
		t.Fatal(err)
	}

	u := unitByPath(t, database, repo, "/lib/A/test.epub")
	if u == nil {
		t.Fatal("seeded unit not found")
	}
	if u.PriorBookStatus != "" {
		t.Fatalf("new unit prior_book_status = %q, want empty", u.PriorBookStatus)
	}

	token, err := repo.Claim(ctx, u.ID, UnmatchedStatePending, UnmatchedStateAdopting)
	if err != nil || token == "" {
		t.Fatalf("claim failed: %v", err)
	}

	// RecordAdoptionProgress saves PriorBookStatus
	rec := AdoptionRecord{BookID: book.ID, PriorBookStatus: models.BookStatusSkipped}
	if ok, err := repo.RecordAdoptionProgress(ctx, u.ID, token, rec); err != nil || !ok {
		t.Fatalf("record adoption progress: ok=%v, err=%v", ok, err)
	}

	u, err = repo.Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.PriorBookStatus != models.BookStatusSkipped {
		t.Fatalf("after progress, prior_book_status = %q, want 'skipped'", u.PriorBookStatus)
	}

	// CompleteAdoption keeps PriorBookStatus
	if ok, err := repo.CompleteAdoption(ctx, u.ID, token, rec); err != nil || !ok {
		t.Fatalf("complete adoption: ok=%v, err=%v", ok, err)
	}
	u, err = repo.Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.PriorBookStatus != models.BookStatusSkipped {
		t.Fatalf("after complete, prior_book_status = %q, want 'skipped'", u.PriorBookStatus)
	}

	// ResetToPending clears PriorBookStatus
	token, err = repo.Claim(ctx, u.ID, UnmatchedStateAdopted, UnmatchedStateUndoing)
	if err != nil || token == "" {
		t.Fatalf("claim for undo failed: %v", err)
	}
	if ok, err := repo.ResetToPending(ctx, u.ID, UnmatchedStateUndoing, token); err != nil || !ok {
		t.Fatalf("reset to pending: ok=%v, err=%v", ok, err)
	}
	u, err = repo.Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.PriorBookStatus != "" {
		t.Fatalf("after reset, prior_book_status = %q, want empty", u.PriorBookStatus)
	}
}
