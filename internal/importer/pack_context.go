package importer

import (
	"context"

	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// packMarker returns the words of a download's release name that make it a
// pack of several books for book, or "" when it is not one or book is itself
// a bundle. It loads the same library context the search used (the book's
// series and its author's other titles) and judges with the same spec, so the
// import guard recognises every pack the automatic search refuses (#3247),
// including one a user grabbed by hand from interactive search. A failed read
// only leaves the shapes that need it unmatched.
func (s *Scanner) packMarker(ctx context.Context, release string, book *models.Book) string {
	var crit indexer.MatchCriteria
	var seriesRead indexer.SeriesReader
	if s.series != nil {
		seriesRead = s.series.GetPrimarySeriesForBook
	}
	var booksRead indexer.AuthorBooksReader
	if s.books != nil {
		booksRead = func(ctx context.Context, authorID int64) ([]models.Book, error) {
			return s.books.ListByAuthorAndUser(ctx, authorID, book.OwnerUserID)
		}
	}
	crit.SetLibraryContext(ctx, *book, seriesRead, booksRead)
	spec := decision.MultiBookPackSpec{SeriesTitle: crit.SeriesTitle, AuthorTitles: crit.AuthorTitles}
	return spec.Marker(decision.Release{Title: release}, *book)
}
