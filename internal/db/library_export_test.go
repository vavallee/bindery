package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A batch larger than one IN list is split into chunks and still returns every
// row grouped by book.
func TestBatchLoads_SpanMoreThanOneChunk(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors, books, editions, series := NewAuthorRepo(database), NewBookRepo(database), NewEditionRepo(database), NewSeriesRepo(database)
	a := &models.Author{ForeignID: "OL-CH-A", Name: "Chunk", SortName: "Chunk", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "S-CH", Title: "Chunked"}
	if err := series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	n := sqliteChunk + 25
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		b := &models.Book{ForeignID: fmt.Sprintf("OL-CH-B%d", i), AuthorID: a.ID, Title: fmt.Sprintf("B%d", i), SortTitle: fmt.Sprintf("b%d", i),
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", MediaType: models.MediaTypeEbook}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := editions.Upsert(ctx, &models.Edition{ForeignID: fmt.Sprintf("E%d", i), BookID: b.ID, Title: b.Title}); err != nil {
			t.Fatal(err)
		}
		if err := series.LinkBook(ctx, s.ID, b.ID, "1", true); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	eds, err := editions.ListByBooks(ctx, ids)
	if err != nil || len(eds) != n {
		t.Fatalf("editions: %d books, err %v, want %d", len(eds), err, n)
	}
	ms, err := series.ListBookSeriesMembershipsByBooks(ctx, ids)
	if err != nil || len(ms) != n {
		t.Fatalf("series: %d books, err %v, want %d", len(ms), err, n)
	}
	if got, err := editions.ListByBooks(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty input: %v %v", got, err)
	}
}
