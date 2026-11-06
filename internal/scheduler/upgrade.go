package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// The ebook upgrade sweep (#2420). The wanted sweep only ever looks at books
// with nothing on disk; this one looks at imported ebooks whose best file ranks
// below their profile's cutoff and searches for a better format.
//
// It is gated twice. A profile must have UpgradeAllowed set, which is off by
// default and was cleared on every existing profile by migration 122, and
// indexer.EbookUpgradeEligible must say there is room to improve: a ticked
// ebook cutoff, every owned file a known ticked ebook format, and the best of
// them below the cutoff. Anything it cannot rank fails closed, so the sweep
// never searches on a guess.
//
// What an upgrade search may grab is narrowed again per release by
// decision.EbookUpgrade, and what the importer may do with the result is
// narrowed by its own checks: the content check runs on the new file before
// the old one is touched.

// upgradeBaseline is what an upgrade search compares releases against.
type upgradeBaseline struct {
	profile *models.QualityProfile
	// paths are the book's recorded ebook files.
	paths []string
}

type upgradeBaselineKey struct{}

// withUpgradeBaseline marks ctx as an upgrade search for a book that already
// has the ebook files in b. searchAndGrabFormat then adds decision.EbookUpgrade
// to its specifications. A nil baseline leaves ctx alone.
func withUpgradeBaseline(ctx context.Context, b *upgradeBaseline) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, upgradeBaselineKey{}, b)
}

func upgradeBaselineFrom(ctx context.Context) *upgradeBaseline {
	b, _ := ctx.Value(upgradeBaselineKey{}).(*upgradeBaseline)
	return b
}

// upgradeSearchCooldown is how long a book is left alone after an upgrade
// search, whether or not the search found anything. The wanted sweep has no
// per-book timestamp to reuse (a wanted book is searched every cycle), so this
// borrows the shape of metadataCooldown: an in-memory map on the scheduler.
// A book that has something to upgrade to is grabbed on the first search and
// is then guarded by its in-flight download, so the wait only matters for the
// book with no better release anywhere.
const upgradeSearchCooldown = 24 * time.Hour

// noteUpgradeSearched records that book id was searched for an upgrade at now.
func (s *Scheduler) noteUpgradeSearched(id int64, now time.Time) {
	s.upgradeSearchedMu.Lock()
	defer s.upgradeSearchedMu.Unlock()
	if s.upgradeSearched == nil {
		s.upgradeSearched = map[int64]time.Time{}
	}
	s.upgradeSearched[id] = now
}

// coolingUpgradeBooks returns the books searched within upgradeSearchCooldown,
// forgetting the ones whose wait has ended.
func (s *Scheduler) coolingUpgradeBooks(now time.Time) map[int64]bool {
	s.upgradeSearchedMu.Lock()
	defer s.upgradeSearchedMu.Unlock()
	out := make(map[int64]bool, len(s.upgradeSearched))
	for id, at := range s.upgradeSearched {
		if now.Sub(at) < upgradeSearchCooldown {
			out[id] = true
		} else {
			delete(s.upgradeSearched, id)
		}
	}
	return out
}

// upgradeSearch is one unit of work for the upgrade branch of the sweep.
type upgradeSearch struct {
	book     models.Book
	baseline *upgradeBaseline
}

// upgradeSearchQueue returns the imported ebooks worth searching again, one
// entry per book, so a book is searched once per cycle however many files it
// holds. It excludes excluded books, books of any other media type (a
// dual-format book is left alone: its pair gating and shared folder are a
// separate problem, and audiobooks are out of scope), books with a grab already
// in flight or searched within upgradeSearchCooldown, and books whose profile
// does not allow an upgrade or leaves no room for one. It returns before
// reading any books when no profile allows upgrades at all. A failed lookup skips the book, never searches it.
func (s *Scheduler) upgradeSearchQueue(ctx context.Context) []upgradeSearch {
	if s.books == nil || s.authors == nil || s.qualityProfiles == nil {
		return nil
	}
	// Stop here on an install where no profile opted in, before reading the
	// library. A failed check searches nothing rather than everything.
	if any, err := s.qualityProfiles.AnyUpgradeAllowed(ctx); err != nil {
		slog.Warn("upgrade sweep: could not check the quality profiles", "error", err)
		return nil
	} else if !any {
		return nil
	}
	imported, err := s.books.ListByStatus(ctx, models.BookStatusImported)
	if err != nil {
		slog.Error("failed to list imported books for the upgrade sweep", "error", err)
		return nil
	}
	if len(imported) == 0 {
		return nil
	}

	// One profile lookup per author, not per book.
	type resolved struct{ profile *models.QualityProfile }
	profiles := map[int64]resolved{}
	profileFor := func(authorID int64) *models.QualityProfile {
		if r, ok := profiles[authorID]; ok {
			return r.profile
		}
		var p *models.QualityProfile
		if a, err := s.authors.GetByID(ctx, authorID); err != nil {
			slog.Warn("upgrade sweep: failed to load author", "author_id", authorID, "error", err)
		} else {
			p = db.ResolveAuthorQualityProfile(ctx, s.qualityProfiles, a)
		}
		profiles[authorID] = resolved{p}
		return p
	}

	cooling := s.coolingUpgradeBooks(time.Now())
	var inFlight map[int64]map[string]bool
	queue := make([]upgradeSearch, 0)
	for _, book := range imported {
		if book.Excluded || book.MediaType != models.MediaTypeEbook || cooling[book.ID] {
			continue
		}
		profile := profileFor(book.AuthorID)
		if profile == nil || !profile.UpgradeAllowed {
			continue
		}
		files, err := s.books.ListFiles(ctx, book.ID)
		if err != nil {
			slog.Warn("upgrade sweep: failed to list book files", "book_id", book.ID, "error", err)
			continue
		}
		var paths []string
		for _, f := range files {
			if f.Format == models.MediaTypeEbook {
				paths = append(paths, f.Path)
			}
		}
		// Is there room to improve at all? The cutoff format itself stands in
		// for the best release a search could find.
		if !indexer.EbookUpgradeEligible(profile, paths, []string{profile.Cutoff}) {
			continue
		}
		// Loaded on the first book that gets this far.
		if inFlight == nil {
			inFlight = s.inFlightFormatsByBook(ctx)
		}
		if len(inFlight[book.ID]) > 0 {
			slog.Debug("skipping upgrade search: a grab is already in flight for this book", "book", book.Title)
			continue
		}
		queue = append(queue, upgradeSearch{book: book, baseline: &upgradeBaseline{profile: profile, paths: paths}})
	}
	return queue
}
