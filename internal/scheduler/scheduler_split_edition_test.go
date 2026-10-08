package scheduler

import (
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestWantedSearchQueue_SkipsSplitEditionPartsOfAnOwnedBook is the #3048
// regression test. An install that pressed Fill on Stormlight before #2524
// holds "The Way of Kings, Part 1" and "Part 2" as wanted rows at 1.1 and 1.2
// next to the imported whole at 1. The sweep kept searching them and could
// download a part of a book already on the shelf. A real novella at a
// fractional position, and a split part with no whole beside it, must still be
// searched.
func TestWantedSearchQueue_SkipsSplitEditionPartsOfAnOwnedBook(t *testing.T) {
	f := newWantedQueueFixture(t)
	series := &models.Series{ForeignID: "hc-series:stormlight", Title: "The Stormlight Archive"}
	seriesRepo := db.NewSeriesRepo(f.database)
	if err := seriesRepo.Create(f.ctx, series); err != nil {
		t.Fatal(err)
	}
	link := func(b *models.Book, pos string) {
		t.Helper()
		if err := seriesRepo.LinkBook(f.ctx, series.ID, b.ID, pos, true); err != nil {
			t.Fatal(err)
		}
	}

	whole := f.book("The Way of Kings", models.MediaTypeEbook)
	if _, err := f.database.ExecContext(f.ctx, "UPDATE books SET status=? WHERE id=?", models.BookStatusImported, whole.ID); err != nil {
		t.Fatal(err)
	}
	link(whole, "1")
	part1 := f.book("The Way of Kings, Part 1", models.MediaTypeEbook)
	link(part1, "1.1")
	part2 := f.book("The Way of Kings, Part 2", models.MediaTypeEbook)
	link(part2, "1.2")
	radiance := f.book("Words of Radiance", models.MediaTypeEbook)
	link(radiance, "2")
	novella := f.book("Edgedancer", models.MediaTypeEbook)
	link(novella, "2.5")
	// Oathbringer's parts with no whole Oathbringer row: they are the volumes.
	lonePart := f.book("Oathbringer, Part 1", models.MediaTypeEbook)
	link(lonePart, "3.1")

	queue := f.queue()
	for _, b := range []*models.Book{part1, part2} {
		if entryFor(queue, b.ID) != nil {
			t.Errorf("%q was queued for search although the whole book is imported", b.Title)
		}
	}
	for _, b := range []*models.Book{radiance, novella, lonePart} {
		if entryFor(queue, b.ID) == nil {
			t.Errorf("%q was dropped from the sweep", b.Title)
		}
	}
}
