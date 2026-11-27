package opds

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// BenchmarkListFeeds times the two list feeds an OPDS reader opens first, on
// libraries of 1,000, 10,000 and 40,000 books with one author per 20 books and
// three quarters of the books on disk (the proportions tools/perf seeds). The
// time per feed should stay level as the library grows.
//
//	go test ./internal/opds -run '^$' -bench ListFeeds -benchtime 20x
func BenchmarkListFeeds(b *testing.B) {
	for _, books := range []int{1000, 10000, 40000} {
		database, err := db.OpenMemory()
		if err != nil {
			b.Fatal(err)
		}
		ctx := context.Background()
		bookRepo, authorRepo := db.NewBookRepo(database), db.NewAuthorRepo(database)
		authorIDs := make([]int64, books/20)
		for i := range authorIDs {
			a := &models.Author{ForeignID: fmt.Sprint("bench:author:", i), Name: fmt.Sprint("Author ", i), SortName: fmt.Sprintf("%05d, Author", (i*7919)%len(authorIDs))}
			if err := authorRepo.Create(ctx, a); err != nil {
				b.Fatal(err)
			}
			authorIDs[i] = a.ID
		}
		for i := 0; i < books; i++ {
			bk := &models.Book{ForeignID: fmt.Sprint("bench:book:", i), AuthorID: authorIDs[i%len(authorIDs)], Title: fmt.Sprint("Book ", i), Status: models.BookStatusWanted}
			if err := bookRepo.Create(ctx, bk); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := database.Exec(`UPDATE books SET monitored = 1, status = 'imported', file_path = '/library/' || id || '.epub' WHERE id % 4 != 0`); err != nil {
			b.Fatal(err)
		}
		// Create stamps the whole library within a second or two. Spread it to
		// four books a second, a rate an import can reach, so the recent feed
		// is not measured on one enormous tie.
		if _, err := database.Exec(`UPDATE books SET updated_at = strftime('%Y-%m-%dT%H:%M:%S', 1790000000 + id / 4, 'unixepoch') || '.' || (id % 4 + 1) || 'Z'`); err != nil {
			b.Fatal(err)
		}
		builder := NewBuilder(Config{PageSize: 50}, bookRepo, authorRepo, db.NewSeriesRepo(database))

		b.Run(fmt.Sprintf("authors/books=%d", books), func(b *testing.B) {
			for b.Loop() {
				f, err := builder.BuildAuthors(ctx, "http://host", 1, 0)
				if err != nil || len(f.Entries) != 50 {
					b.Fatalf("entries = %d, err = %v", len(f.Entries), err)
				}
			}
		})
		b.Run(fmt.Sprintf("recent/books=%d", books), func(b *testing.B) {
			for b.Loop() {
				f, err := builder.BuildRecent(ctx, "http://host", 0)
				if err != nil || len(f.Entries) != 50 {
					b.Fatalf("entries = %d, err = %v", len(f.Entries), err)
				}
			}
		})
		database.Close()
	}
}
