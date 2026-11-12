package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

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

// SeriesAuthorEvidence is what an importer reads before filing a book by one
// author into a same titled series it did not create (#2173).
type SeriesAuthorEvidence struct {
	// Exists is false when the series is gone, deleted or merged away since
	// the caller listed it. Every other field is then zero.
	Exists bool
	// Books is how many books the series holds, ByAuthor how many of them are
	// by the author asked about.
	Books, ByAuthor int
	// HardcoverAuthorName is the author the series' Hardcover link names, or
	// "" when it has no link or the link names nobody.
	HardcoverAuthorName string
	// AuthorName is the name of the author asked about, read in the same
	// query so the caller can compare it with HardcoverAuthorName.
	AuthorName string
}

// SeriesAuthorEvidence reports, in one query, whether seriesID still exists,
// how many books it holds and how many of them are by authorID, and the author
// its Hardcover link names. A series that holds only other authors' books, or
// an empty one linked to another author's Hardcover series, is a different
// series that happens to share a name.
func (r *SeriesRepo) SeriesAuthorEvidence(ctx context.Context, seriesID, authorID int64) (SeriesAuthorEvidence, error) {
	var ev SeriesAuthorEvidence
	err := r.db.QueryRowContext(ctx,
		`SELECT
		     (SELECT COUNT(*) FROM series_books sb WHERE sb.series_id = s.id),
		     (SELECT COUNT(*) FROM series_books sb JOIN books b ON b.id = sb.book_id
		       WHERE sb.series_id = s.id AND b.author_id = ?),
		     COALESCE((SELECT l.hardcover_author_name FROM series_hardcover_links l WHERE l.series_id = s.id), ''),
		     COALESCE((SELECT a.name FROM authors a WHERE a.id = ?), '')
		   FROM series s WHERE s.id = ?`, authorID, authorID, seriesID).
		Scan(&ev.Books, &ev.ByAuthor, &ev.HardcoverAuthorName, &ev.AuthorName)
	if errors.Is(err, sql.ErrNoRows) {
		return SeriesAuthorEvidence{}, nil
	}
	if err != nil {
		return SeriesAuthorEvidence{}, fmt.Errorf("series %d author evidence: %w", seriesID, err)
	}
	ev.Exists = true
	return ev, nil
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
