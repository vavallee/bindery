package calibre

import (
	"context"
	"log/slog"
	"strings"
)

// CoverRepairStats reports what one RepairLocalCovers pass did.
type CoverRepairStats struct {
	// EditionsRewritten is the number of edition rows whose host path became
	// a stored-cover reference.
	EditionsRewritten int
	// BooksRewritten is the number of book rows given a stored-cover
	// reference, either from their own host path or from one of their
	// editions when the book had no cover at all.
	BooksRewritten int
	// Unreadable counts rows left alone because the path they hold could
	// not be read or was not an image. They are retried on the next start
	// and rewritten by the next library import.
	Unreadable int
}

// RepairLocalCovers rewrites rows left behind by importers older than #2564,
// which stored each Calibre book's cover.jpg on the edition as an absolute
// host path. For every such edition the file is copied into the covers store
// and the row repointed at the reference; the owning book gets the same
// reference when it has no cover of its own. Book rows holding a host path
// are swept the same way.
//
// It is a filesystem operation, so it runs at startup rather than as a
// schema migration: a library volume that is not mounted yet must not turn
// a migration failure into a boot failure, and a row it cannot fix this
// time is simply still there next time. Idempotent; a second pass finds
// nothing to do.
func (i *Importer) RepairLocalCovers(ctx context.Context) (CoverRepairStats, error) {
	var stats CoverRepairStats
	if i.covers == nil {
		return stats, nil
	}

	editions, err := i.editions.ListWithLocalImagePath(ctx)
	if err != nil {
		return stats, err
	}
	// First stored reference per book, so a book with four formats gets its
	// cover from whichever edition's file was readable.
	byBook := make(map[int64]string)
	for _, e := range editions {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		ref, err := i.covers.Put(e.ImageURL)
		if err != nil {
			slog.Debug("calibre cover repair: edition cover unreadable", "edition_id", e.ID, "path", e.ImageURL, "error", err)
			stats.Unreadable++
			continue
		}
		if err := i.editions.SetImageURL(ctx, e.ID, ref); err != nil {
			return stats, err
		}
		stats.EditionsRewritten++
		if _, seen := byBook[e.BookID]; !seen {
			byBook[e.BookID] = ref
		}
	}

	books, err := i.books.ListWithLocalImagePath(ctx)
	if err != nil {
		return stats, err
	}
	for _, b := range books {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		ref, err := i.covers.Put(b.ImageURL)
		if err != nil {
			fromEdition, ok := byBook[b.ID]
			if !ok {
				slog.Debug("calibre cover repair: book cover unreadable", "book_id", b.ID, "path", b.ImageURL, "error", err)
				stats.Unreadable++
				continue
			}
			ref = fromEdition
		}
		if err := i.books.SetImageURL(ctx, b.ID, ref); err != nil {
			return stats, err
		}
		stats.BooksRewritten++
		delete(byBook, b.ID)
	}

	for bookID, ref := range byBook {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		b, err := i.books.GetByID(ctx, bookID)
		if err != nil {
			return stats, err
		}
		if b == nil || strings.TrimSpace(b.ImageURL) != "" {
			continue
		}
		if err := i.books.SetImageURL(ctx, bookID, ref); err != nil {
			return stats, err
		}
		stats.BooksRewritten++
	}

	if stats.EditionsRewritten > 0 || stats.BooksRewritten > 0 || stats.Unreadable > 0 {
		slog.Info("calibre cover repair finished",
			"editions", stats.EditionsRewritten, "books", stats.BooksRewritten, "unreadable", stats.Unreadable)
	}
	return stats, nil
}
