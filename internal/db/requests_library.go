package db

import (
	"context"
	"fmt"

	"github.com/vavallee/bindery/internal/textutil"
)

// LibraryProjectionRow is the read only view of one library book a requester
// may see (security review item S1). It is selected column by column, so
// file paths, owner ids and everything else on a book row are never read, let
// alone sent. The API layer copies it field by field into its own response
// type, which a reflection test pins to an allow list.
type LibraryProjectionRow struct {
	ID             int64
	Title          string
	AuthorName     string
	SeriesTitle    string
	SeriesPosition string
	ImageURL       string
	Status         string
	HasEbook       bool
	HasAudiobook   bool
}

// ListLibraryProjection returns one page of non excluded books in title
// order, with the total for the same filter. scopeUserID follows
// BookListFilter.UserID: 0 is the whole library, otherwise the user's own
// books plus unowned ones. search matches the title, the author name or the
// name of a series the book is in, the same way the Books page does (#3462).
func (r *RequestRepo) ListLibraryProjection(ctx context.Context, scopeUserID int64, search string, limit, offset int) ([]LibraryProjectionRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args := QueryScopeForIncludingNull("books.owner_user_id", "WHERE books.excluded = 0", scopeUserID)
	if folded := textutil.FoldForSearch(search); folded != "" {
		for _, tok := range searchTokens(folded) {
			like := "%" + escapeLike(tok) + "%"
			where += " AND (books.search_key LIKE ? ESCAPE '\\' OR COALESCE(au.search_key, '') LIKE ? ESCAPE '\\'" +
				" OR EXISTS (SELECT 1 FROM series_books sb JOIN series s ON s.id = sb.series_id" +
				" WHERE sb.book_id = books.id AND s.search_key LIKE ? ESCAPE '\\'))"
			args = append(args, like, like, like)
		}
	}
	const from = " FROM books LEFT JOIN authors au ON au.id = books.author_id "

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*)"+from+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count library projection: %w", err)
	}

	// The series is the book's primary series, ordered the way
	// SeriesRepo.GetPrimarySeriesForBook orders it.
	// #nosec G202 -- from and bookTitleOrder are constants, and where is built from QueryScopeForIncludingNull and one fixed LIKE clause per search token; the scope id and the escaped search patterns are bound args
	q := `SELECT books.id, books.title, COALESCE(au.name, ''),
		COALESCE((SELECT s.title FROM series_books sb JOIN series s ON s.id = sb.series_id
		          WHERE sb.book_id = books.id
		          ORDER BY sb.primary_series DESC, CASE WHEN trim(sb.position_in_series) = '' THEN 1 ELSE 0 END, sb.series_id
		          LIMIT 1), ''),
		COALESCE((SELECT sb.position_in_series FROM series_books sb
		          WHERE sb.book_id = books.id
		          ORDER BY sb.primary_series DESC, CASE WHEN trim(sb.position_in_series) = '' THEN 1 ELSE 0 END, sb.series_id
		          LIMIT 1), ''),
		COALESCE(books.image_url, ''), books.status,
		EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = books.id AND f.format = 'ebook'),
		EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = books.id AND f.format = 'audiobook')` +
		from + where + " ORDER BY " + bookTitleOrder + " LIMIT ? OFFSET ?"
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.QueryContext(ctx, q, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list library projection: %w", err)
	}
	defer rows.Close()
	out := []LibraryProjectionRow{}
	for rows.Next() {
		var row LibraryProjectionRow
		if err := rows.Scan(&row.ID, &row.Title, &row.AuthorName, &row.SeriesTitle, &row.SeriesPosition,
			&row.ImageURL, &row.Status, &row.HasEbook, &row.HasAudiobook); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}
