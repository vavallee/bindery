package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// Queries behind the duplicate review (#1970, #2999). The detection itself
// lives in internal/duplicates, which this package must never import; these
// methods only load the rows it reads, in as few round trips as possible, so
// the library-wide view costs a fixed number of queries however many authors
// and groups there are.

// duplicateEvidenceChunk bounds the IN list of one evidence query, well under
// SQLite's bound-parameter limit.
const duplicateEvidenceChunk = 500

// ListForDuplicateScan returns a thin row for every book whose author the
// caller may see, ordered by author: only ID, AuthorID, Title and Excluded are
// set, and nothing else on the returned books is loaded. It is the input of
// the library-wide duplicate scan, which reads titles only and hydrates the
// full rows of the groups it actually returns afterwards, so a library of
// tens of thousands of books never has its descriptions and file columns in
// memory at once.
//
// ownerUserID scopes by the AUTHOR's owner, the same row the per-author
// window checks with auth.CheckOwnership: 0 is unscoped, anything else keeps
// authors owned by that user plus unowned ones. A book whose author row is
// gone is left out, as no author window can show it either.
//
// The second result maps each returned author ID to its name.
func (r *BookRepo) ListForDuplicateScan(ctx context.Context, ownerUserID int64) ([]models.Book, map[int64]string, error) {
	const sel = `SELECT books.id, books.author_id, COALESCE(books.title, ''), books.excluded, COALESCE(au.name, '')
		FROM books JOIN authors au ON au.id = books.author_id `
	where, args := QueryScopeForIncludingNull("au.owner_user_id", "", ownerUserID)
	q := sel + where + ` ORDER BY books.author_id, books.id` // #nosec G202 -- where is a fixed predicate from QueryScopeForIncludingNull; the user id stays a bound arg
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list books for duplicate scan: %w", err)
	}
	defer rows.Close()
	var books []models.Book
	names := map[int64]string{}
	for rows.Next() {
		var b models.Book
		var name string
		if err := rows.Scan(&b.ID, &b.AuthorID, &b.Title, &b.Excluded, &name); err != nil {
			return nil, nil, fmt.Errorf("scan book for duplicate scan: %w", err)
		}
		books = append(books, b)
		names[b.AuthorID] = name
	}
	return books, names, rows.Err()
}

// DuplicateScanStamp is a cheap fingerprint of everything the library-wide
// duplicate scan reads: the books (count, highest id, which are excluded,
// which author each belongs to, title lengths, latest update), the series
// links and their positions, and the authors (count, owners, latest update).
// The library-wide view caches its group list under this stamp, so paging
// does not rescan the library while an exclusion, an import, a new book, a
// series relink or an ownership change all produce a new stamp. It is one
// aggregate query over three tables, with no row data returned.
//
// It is a fingerprint, not a hash: an edit that keeps every aggregate equal
// (renaming a title to another of the same length without touching
// updated_at) is not seen, which is why the cache also expires on a short
// timer.
func (r *BookRepo) DuplicateScanStamp(ctx context.Context) (string, error) {
	var stamp string
	err := r.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) || ':' || COALESCE(MAX(id), 0) || ':' || TOTAL(excluded * id) || ':' ||
		        TOTAL(author_id * 7 + id) || ':' || TOTAL(LENGTH(title) * id) || ':' || COALESCE(MAX(updated_at), '')
		   FROM books)
		|| '|' ||
		(SELECT COUNT(*) || ':' || TOTAL(series_id * 1000003 + book_id) || ':' ||
		        TOTAL(LENGTH(position_in_series) * book_id) || ':' || TOTAL(CAST(position_in_series AS REAL) * book_id)
		   FROM series_books)
		|| '|' ||
		(SELECT COUNT(*) || ':' || TOTAL(COALESCE(owner_user_id, 0) * id) || ':' || COALESCE(MAX(updated_at), '')
		   FROM authors)`).Scan(&stamp)
	if err != nil {
		return "", fmt.Errorf("duplicate scan stamp: %w", err)
	}
	return stamp, nil
}

// DuplicateEvidence is what the duplicate review shows about one book beyond
// its row: the files Bindery holds for it and the identifiers its editions
// carry. Values are raw as stored; internal/duplicates normalizes them.
type DuplicateEvidence struct {
	Files []models.BookFile
	ISBNs []string
	ASINs []string
}

// ListDuplicateEvidence loads DuplicateEvidence for the given books in two
// queries per chunk of IDs (book_files, then editions), never one per book.
// Books with nothing recorded are absent from the map.
func (r *BookRepo) ListDuplicateEvidence(ctx context.Context, bookIDs []int64) (map[int64]DuplicateEvidence, error) {
	out := make(map[int64]DuplicateEvidence, len(bookIDs))
	for start := 0; start < len(bookIDs); start += duplicateEvidenceChunk {
		end := min(start+duplicateEvidenceChunk, len(bookIDs))
		chunk := bookIDs[start:end]

		files, err := r.files.ListByBooks(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for id, fs := range files {
			ev := out[id]
			ev.Files = append(ev.Files, fs...)
			out[id] = ev
		}

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "?"
			args[i] = id
		}
		q := `SELECT DISTINCT book_id, COALESCE(isbn_13, ''), COALESCE(isbn_10, ''), COALESCE(asin, '')
			FROM editions WHERE book_id IN (` + strings.Join(placeholders, ",") + `)` // #nosec G202 -- placeholders are fixed ? tokens; IDs stay bound args
		if err := func() error {
			rows, err := r.db.QueryContext(ctx, q, args...)
			if err != nil {
				return fmt.Errorf("list edition identifiers: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var isbn13, isbn10, asin string
				if err := rows.Scan(&id, &isbn13, &isbn10, &asin); err != nil {
					return fmt.Errorf("scan edition identifiers: %w", err)
				}
				ev := out[id]
				for _, v := range []string{isbn13, isbn10} {
					if v != "" {
						ev.ISBNs = append(ev.ISBNs, v)
					}
				}
				if asin != "" {
					ev.ASINs = append(ev.ASINs, asin)
				}
				out[id] = ev
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListBookSeriesMembershipsForOwner is ListBookSeriesMembershipsByAuthor for
// every author the caller may see, scoped exactly as ListForDuplicateScan
// scopes books (by the author's owner; 0 is unscoped), in one query.
func (r *SeriesRepo) ListBookSeriesMembershipsForOwner(ctx context.Context, ownerUserID int64) (map[int64][]BookSeriesMembership, error) {
	where, args := QueryScopeForIncludingNull("au.owner_user_id", "", ownerUserID)
	out, err := r.scanBookSeriesMemberships(ctx, `
		SELECT sb.book_id, sb.series_id, COALESCE(s.foreign_id, ''), COALESCE(s.title, ''),
		       COALESCE(sb.position_in_series, ''), COALESCE(sb.primary_series, 0)
		FROM books b
		JOIN authors au ON au.id = b.author_id
		JOIN series_books sb ON sb.book_id = b.id
		JOIN series s ON s.id = sb.series_id `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list series memberships for owner %d: %w", ownerUserID, err)
	}
	return out, nil
}
