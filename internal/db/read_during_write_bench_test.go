package db

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// BenchmarkBookPageDuringWrites is the #2147 case at the database layer: one
// page of the Books list, read from several goroutines, on a file database
// with the production pragmas. "idle" has no writer. "writing" runs one
// goroutine that inserts history rows in transactions of 50, the shape of an
// import or a library scan recording what it did.
//
// With the pool pinned to one connection, every read waits for the
// transaction that holds it, so the two sub-benchmarks are the before and
// after of any change to SetMaxOpenConns. ns/op is per page read.
func BenchmarkBookPageDuringWrites(b *testing.B) {
	for _, writing := range []bool{false, true} {
		name := "idle"
		if writing {
			name = "writing"
		}
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			database, err := Open(filepath.Join(b.TempDir(), "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = database.Close() })
			authors, books := NewAuthorRepo(database), NewBookRepo(database)
			author := models.Author{ForeignID: "OL-A-1", Name: "Bench Author", SortName: "Author, Bench"}
			if err := authors.Create(ctx, &author); err != nil {
				b.Fatal(err)
			}
			const total = 2000
			for i := range total {
				bk := models.Book{ForeignID: fmt.Sprintf("OL-B-%d", i), AuthorID: author.ID, Title: fmt.Sprintf("Bench Title %05d", i), Status: models.BookStatusWanted}
				if err := books.Create(ctx, &bk); err != nil {
					b.Fatal(err)
				}
			}

			stop := make(chan struct{})
			var wg sync.WaitGroup
			if writing {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := 0; ; n++ {
						select {
						case <-stop:
							return
						default:
						}
						tx, err := database.BeginTx(ctx, nil)
						if err != nil {
							return
						}
						for i := range 50 {
							e := models.HistoryEvent{EventType: models.HistoryEventBookImported, SourceTitle: fmt.Sprintf("bench %d %d", n, i)}
							if err := insertHistoryEvent(ctx, tx, &e); err != nil {
								_ = tx.Rollback()
								return
							}
						}
						if err := tx.Commit(); err != nil {
							return
						}
					}
				}()
			}

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				offset := 0
				for pb.Next() {
					if _, _, err := books.ListPageFiltered(ctx, BookListFilter{Sort: "title-az"}, 50, offset%(total-50)); err != nil {
						b.Error(err)
						return
					}
					offset += 50
				}
			})
			b.StopTimer()
			close(stop)
			wg.Wait()
		})
	}
}
