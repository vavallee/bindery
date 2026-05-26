package db

import (
	"context"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAuthorMonitoredSeriesRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{
		ForeignID: "OL-A1", Name: "Frank Herbert", SortName: "Herbert, Frank",
		MetadataProvider: "openlibrary", Monitored: true, MonitorMode: models.AuthorMonitorModeSeries,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}

	s1 := &models.Series{ForeignID: "ol-series:dune", Title: "Dune"}
	s2 := &models.Series{ForeignID: "ol-series:dosadi", Title: "ConSentiency"}
	if err := seriesRepo.CreateOrGet(ctx, s1); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.CreateOrGet(ctx, s2); err != nil {
		t.Fatal(err)
	}

	// Empty by default — returns [] not nil.
	got, err := authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if got == nil {
		t.Fatalf("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 ids, got %v", got)
	}

	// Set two.
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, []int64{s1.ID, s2.ID}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ = authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []int64{s1.ID, s2.ID}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after set: got %v want %v", got, want)
	}

	// Replace selection — old rows must be gone.
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, []int64{s1.ID}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	if len(got) != 1 || got[0] != s1.ID {
		t.Fatalf("after replace: got %v want [%d]", got, s1.ID)
	}

	// Duplicates in input are deduped.
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, []int64{s2.ID, s2.ID, s2.ID}); err != nil {
		t.Fatalf("dup: %v", err)
	}
	got, _ = authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	if len(got) != 1 || got[0] != s2.ID {
		t.Fatalf("after dedup: got %v want [%d]", got, s2.ID)
	}

	// Clear.
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	if len(got) != 0 {
		t.Fatalf("after clear: got %v", got)
	}
}

func TestAuthorMonitoredSeriesCascadesOnDelete(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{ForeignID: "OL-A2", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "ol-series:x", Title: "X"}
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, []int64{s.ID}); err != nil {
		t.Fatal(err)
	}

	// Deleting the series should drop the join row via FK CASCADE — and
	// listing for the author should now be empty without erroring.
	if err := seriesRepo.Delete(ctx, s.ID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	got, err := authorRepo.ListMonitoredSeriesIDs(ctx, author.ID)
	if err != nil {
		t.Fatalf("list after series delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected cascade to clear join row, got %v", got)
	}

	// Re-seed and verify the author-side cascade too.
	if err := seriesRepo.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.SetMonitoredSeriesIDs(ctx, author.ID, []int64{s.ID}); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.Delete(ctx, author.ID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	// The join row should be gone. Easiest cross-check: re-creating the
	// author and listing returns nothing.
	author2 := &models.Author{ForeignID: "OL-A2", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author2); err != nil {
		t.Fatal(err)
	}
	got2, _ := authorRepo.ListMonitoredSeriesIDs(ctx, author2.ID)
	if len(got2) != 0 {
		t.Fatalf("expected empty after author delete cascade, got %v", got2)
	}
}

func TestListBookSeriesByAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	author := &models.Author{ForeignID: "OL-AX", Name: "X", SortName: "X", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	b1 := &models.Book{ForeignID: "OL-W1", AuthorID: author.ID, Title: "B1", SortTitle: "B1", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	b2 := &models.Book{ForeignID: "OL-W2", AuthorID: author.ID, Title: "B2", SortTitle: "B2", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	b3 := &models.Book{ForeignID: "OL-W3", AuthorID: author.ID, Title: "B3", SortTitle: "B3", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	for _, b := range []*models.Book{b1, b2, b3} {
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	sA := &models.Series{ForeignID: "ol-series:A", Title: "A"}
	sB := &models.Series{ForeignID: "ol-series:B", Title: "B"}
	if err := seriesRepo.CreateOrGet(ctx, sA); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.CreateOrGet(ctx, sB); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, sA.ID, b1.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, sA.ID, b2.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, sB.ID, b2.ID, "1", false); err != nil {
		t.Fatal(err)
	}
	// b3 is standalone.

	got, err := seriesRepo.ListBookSeriesByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got[b1.ID]) != 1 || got[b1.ID][0] != sA.ID {
		t.Errorf("b1: want [sA], got %v", got[b1.ID])
	}
	if len(got[b2.ID]) != 2 {
		t.Errorf("b2: want 2 series, got %v", got[b2.ID])
	}
	if len(got[b3.ID]) != 0 {
		t.Errorf("b3 should not be in map, got %v", got[b3.ID])
	}
}

func TestSeriesListByAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	seriesRepo := NewSeriesRepo(database)

	a1 := &models.Author{ForeignID: "OL-1", Name: "Author One", SortName: "One, Author", MetadataProvider: "openlibrary"}
	a2 := &models.Author{ForeignID: "OL-2", Name: "Author Two", SortName: "Two, Author", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, a1); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.Create(ctx, a2); err != nil {
		t.Fatal(err)
	}

	b1 := &models.Book{ForeignID: "OL-B1", AuthorID: a1.ID, Title: "X", SortTitle: "X", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	b2 := &models.Book{ForeignID: "OL-B2", AuthorID: a2.ID, Title: "Y", SortTitle: "Y", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	if err := bookRepo.Create(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.Create(ctx, b2); err != nil {
		t.Fatal(err)
	}

	sA := &models.Series{ForeignID: "ol-series:a1", Title: "A1"}
	sB := &models.Series{ForeignID: "ol-series:a2", Title: "A2"}
	if err := seriesRepo.CreateOrGet(ctx, sA); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.CreateOrGet(ctx, sB); err != nil {
		t.Fatal(err)
	}
	_ = seriesRepo.LinkBook(ctx, sA.ID, b1.ID, "1", true)
	_ = seriesRepo.LinkBook(ctx, sB.ID, b2.ID, "1", true)

	got, err := seriesRepo.ListByAuthor(ctx, a1.ID)
	if err != nil {
		t.Fatalf("list by author: %v", err)
	}
	if len(got) != 1 || got[0].ID != sA.ID {
		t.Fatalf("a1: want [sA], got %v", got)
	}

	got2, err := seriesRepo.ListByAuthor(ctx, a2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].ID != sB.ID {
		t.Fatalf("a2: want [sB], got %v", got2)
	}
}
