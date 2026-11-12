package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// SeriesTitleKey is the key two series titles share when they name the same
// series. The Audiobookshelf and Calibre imports both match an incoming series
// to an existing row by it (#2173), so they agree on what "the same series"
// is: a series one importer created is the series the other one finds. It is
// the book dedup key, which folds case, punctuation, a leading article and a
// trailing bracketed note.
func SeriesTitleKey(title string) string {
	return indexer.CanonicalDedupKey(title)
}

// ListTitles returns the id, foreign id and title of every series, without the
// extras List hydrates. It is the cheap read behind an importer's per run
// title index.
func (r *SeriesRepo) ListTitles(ctx context.Context) ([]models.Series, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, foreign_id, title FROM series ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list series titles: %w", err)
	}
	defer rows.Close()
	var out []models.Series
	for rows.Next() {
		var s models.Series
		if err := rows.Scan(&s.ID, &s.ForeignID, &s.Title); err != nil {
			return nil, fmt.Errorf("scan series title: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SeriesAuthorEvidence reports how many books seriesID holds and how many of
// them are by authorID. An importer deciding whether a same titled series is
// the one it is filing a book into reads it: a series that holds only other
// authors' books is a different series that happens to share a name.
func (r *SeriesRepo) SeriesAuthorEvidence(ctx context.Context, seriesID, authorID int64) (books, byAuthor int, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN b.author_id = ? THEN 1 ELSE 0 END), 0)
		   FROM series_books sb JOIN books b ON b.id = sb.book_id
		  WHERE sb.series_id = ?`, authorID, seriesID).Scan(&books, &byAuthor)
	if err != nil {
		return 0, 0, fmt.Errorf("series %d author evidence: %w", seriesID, err)
	}
	return books, byAuthor, nil
}

// AddAlias records foreignID as another id for seriesID in series_aliases, the
// table a merge writes (#2554), so every lookup through seriesByForeignIDSQL
// and every CreateOrGet with that id lands on seriesID instead of creating a
// row. It reports whether the alias was written.
//
// It keeps the invariant refuseAlias guards from the other side: an alias
// never equals a live series foreign id, so an id some series already carries
// is left alone. An id that is already an alias keeps the series it names. Both
// checks are part of the INSERT, so a concurrent create cannot slip between a
// lookup and the write.
func (r *SeriesRepo) AddAlias(ctx context.Context, foreignID string, seriesID int64) (bool, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" || seriesID == 0 {
		return false, fmt.Errorf("add series alias %q for %d: empty id", foreignID, seriesID)
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO series_aliases (foreign_id, series_id)
		 SELECT ?, id FROM series WHERE id = ?
		    AND NOT EXISTS (SELECT 1 FROM series WHERE foreign_id = ?)`,
		foreignID, seriesID, foreignID)
	if err != nil {
		return false, fmt.Errorf("add series alias %q for %d: %w", foreignID, seriesID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
