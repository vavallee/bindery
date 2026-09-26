package indexer

import "context"

// SearchOrigin names the thing that started a search. Every automatic search
// funnels through Scheduler.SearchAndGrabBook, which cannot otherwise tell a
// scheduled wanted sweep apart from a bulk action or a series fill, so a log
// line saying "search finished, nothing found" left the user unable to answer
// "did the sweep I just triggered actually run?" (#2154).
//
// It rides on the context rather than a parameter because the call chain
// crosses package boundaries (api handlers hold a BookSearcher interface, not
// a *Scheduler) and because the background pools in api/bulk.go already carry
// their own context into the goroutine that runs the search.
type SearchOrigin string

const (
	// OriginScheduled is the periodic wanted sweep.
	OriginScheduled SearchOrigin = "scheduled"
	// OriginBulk is POST /api/v1/book/bulk or /api/v1/wanted/bulk.
	OriginBulk SearchOrigin = "bulk"
	// OriginSeriesFill is the series Fill action.
	OriginSeriesFill SearchOrigin = "series-fill"
	// OriginAuthor is an author-level search or an author refresh that found
	// new books.
	OriginAuthor SearchOrigin = "author"
	// OriginBook is a single-book search started from the book page.
	OriginBook SearchOrigin = "book"
	// OriginAdd is the search an explicit "add this book" asked for with its
	// search on add flag. It was tagged OriginAuthor until #2742, which is
	// where the overload showed: the other OriginAuthor site is the catalogue
	// sync fanning out over works it has just discovered on its own, and the
	// two need opposite answers to "did a user ask for this search".
	OriginAdd SearchOrigin = "add"
	// OriginRecommendation is an accepted recommendation.
	OriginRecommendation SearchOrigin = "recommendation"
	// OriginListSync is a Hardcover import-list sync that made a book wanted.
	OriginListSync SearchOrigin = "list-sync"
	// OriginRequeue is the automatic re-search after a stalled download was
	// removed and blocklisted.
	OriginRequeue SearchOrigin = "requeue"
	// OriginUnknown is the fallback for a caller that did not set one. It is a
	// real value rather than an empty string so the log line always carries the
	// field and a missing origin reads as a gap rather than as absence.
	OriginUnknown SearchOrigin = "unknown"
)

// Automatic reports whether a search with this origin was started by Bindery
// on its own rather than by a user action on a specific book.
//
// It is the classification the author monitoring rule reads (#2742).
// Unmonitoring an author means "stop reaching for their books by yourself",
// and it must not mean "refuse to search when I press the button", so the
// scheduler suppresses the automatic origins and leaves the rest alone.
//
// The default is automatic, deliberately. A new origin is guarded until
// somebody decides it carries user intent, and OriginUnknown, which is what a
// caller that never tagged its context reports as, lands there too: forgetting
// to say who you are is not evidence that a user asked for anything.
func (o SearchOrigin) Automatic() bool {
	switch o {
	case OriginBook, OriginAdd, OriginBulk, OriginSeriesFill, OriginRecommendation:
		return false
	default:
		return true
	}
}

type searchOriginKey struct{}

// WithSearchOrigin returns ctx tagged with the origin of the search about to run.
func WithSearchOrigin(ctx context.Context, o SearchOrigin) context.Context {
	return context.WithValue(ctx, searchOriginKey{}, o)
}

// SearchOriginFrom returns the origin ctx was tagged with, or OriginUnknown.
func SearchOriginFrom(ctx context.Context) SearchOrigin {
	if o, ok := ctx.Value(searchOriginKey{}).(SearchOrigin); ok && o != "" {
		return o
	}
	return OriginUnknown
}
