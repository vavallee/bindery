package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// sqliteChunk is how many ids go into one IN list. SQLite builds older than
// 3.32 cap bound variables at 999, so batch loads stay under that.
const sqliteChunk = 500

// ExportBookRow is the part of a book the library export reads from the books
// table: nothing beyond the id, title and the author's name.
type ExportBookRow struct {
	ID         int64
	Title      string
	AuthorID   int64
	AuthorName string
}

// ListExportPage returns up to limit non excluded books with id > afterID, in
// id order, scoped exactly as ListPageFiltered scopes the books list: userID 0
// is unscoped, any other value sees that user's own and unowned books. It is a
// keyset page, so a book added or removed between two pages never shifts the
// next one.
func (r *BookRepo) ListExportPage(ctx context.Context, afterID int64, limit int, userID int64) ([]ExportBookRow, error) {
	where, args := QueryScopeForIncludingNull("books.owner_user_id", "WHERE books.excluded = 0 AND books.id > ?", userID, afterID)
	q := `
		SELECT books.id, books.title, books.author_id, COALESCE(au.name, '')
		FROM books LEFT JOIN authors au ON au.id = books.author_id ` + where + `
		ORDER BY books.id LIMIT ?` // #nosec G202 -- where is built from fixed predicates; every value stays a bound arg
	rows, err := r.db.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list export page: %w", err)
	}
	defer rows.Close()
	var out []ExportBookRow
	for rows.Next() {
		var b ExportBookRow
		if err := rows.Scan(&b.ID, &b.Title, &b.AuthorID, &b.AuthorName); err != nil {
			return nil, fmt.Errorf("scan export row: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListByBooks returns the editions of every given book, grouped by book id and
// ordered by edition id, in one query per chunk of ids rather than one per
// book. A book with no editions is absent from the map.
func (r *EditionRepo) ListByBooks(ctx context.Context, bookIDs []int64) (map[int64][]models.Edition, error) {
	out := make(map[int64][]models.Edition, len(bookIDs))
	for start := 0; start < len(bookIDs); start += sqliteChunk {
		chunk := bookIDs[start:min(start+sqliteChunk, len(bookIDs))]
		placeholders, args := inList(chunk)
		rows, err := r.exec.QueryContext(ctx, `
			SELECT id, foreign_id, book_id, title, isbn_13, isbn_10, asin, publisher,
			       publish_date, format, num_pages, language, image_url, is_ebook,
			       edition_info, monitored, created_at, updated_at, narrators, edition_type
			FROM editions WHERE book_id IN (`+placeholders+`) ORDER BY id`, args...) // #nosec G202 -- placeholders are fixed ? tokens; IDs stay bound args
		if err != nil {
			return nil, fmt.Errorf("list editions by books: %w", err)
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				e, err := scanEditionFrom(rows)
				if err != nil {
					return fmt.Errorf("scan edition: %w", err)
				}
				out[e.BookID] = append(out[e.BookID], e)
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListBookSeriesMembershipsByBooks returns the series memberships of the given
// books, keyed by book id, in one query per chunk of ids. Within a book the
// primary series comes first, then by series id.
func (r *SeriesRepo) ListBookSeriesMembershipsByBooks(ctx context.Context, bookIDs []int64) (map[int64][]BookSeriesMembership, error) {
	out := make(map[int64][]BookSeriesMembership, len(bookIDs))
	for start := 0; start < len(bookIDs); start += sqliteChunk {
		chunk := bookIDs[start:min(start+sqliteChunk, len(bookIDs))]
		placeholders, args := inList(chunk)
		got, err := r.scanBookSeriesMemberships(ctx, `
			SELECT sb.book_id, sb.series_id, COALESCE(s.foreign_id, ''), COALESCE(s.title, ''),
			       COALESCE(sb.position_in_series, ''), COALESCE(sb.primary_series, 0)
			FROM series_books sb
			JOIN series s ON s.id = sb.series_id
			WHERE sb.book_id IN (`+placeholders+`)
			ORDER BY sb.book_id, COALESCE(sb.primary_series, 0) DESC, sb.series_id`, args...) // #nosec G202 -- placeholders are fixed ? tokens; IDs stay bound args
		if err != nil {
			return nil, fmt.Errorf("list series memberships by books: %w", err)
		}
		for id, ms := range got {
			out[id] = append(out[id], ms...)
		}
	}
	return out, nil
}

func inList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}
