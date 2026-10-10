package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/seriesmatch"
)

// SplitEditionPart is a local book that is one part of a split edition of
// another book in the same series (#3048): "The Way of Kings, Part 1" at 1.1
// while "The Way of Kings" itself sits at 1.
type SplitEditionPart struct {
	SeriesID    int64
	BookID      int64
	Title       string
	Monitored   bool
	WholeBookID int64
	WholeTitle  string
}

// ListCoveredSplitEditionParts returns the books that are split edition parts
// of a whole book the library already owns or is already looking for. A
// seriesID of 0 covers every series.
//
// The rule is seriesmatch.SplitEditionPartOf, the same two signals the series
// diff uses for catalogue rows (#2524): a fractional position under the whole's
// integer position, and the whole's title plus a "Part N" marker. Here it is
// applied to rows already stored, which is what an install that pressed Fill
// before #2524 has.
//
// Only a part that is not imported is reported: an imported part is already on
// disk and there is nothing to stop searching for. The whole has to be a book
// the user actually has or wants, so it must be imported or monitored, and not
// excluded — the WHERE clause below and api.eligibleSplitEditionWhole
// (internal/api/series.go) apply the identical rule to the catalogue diff;
// an eligibility change here needs the same change made there. Both rows
// must belong to the same owner, so one user's library never decides what
// another user's sweep searches for.
func (r *BookRepo) ListCoveredSplitEditionParts(ctx context.Context, seriesID int64) ([]SplitEditionPart, error) {
	query := `
		SELECT sp.series_id, sp.book_id, sp.position_in_series, bp.title, bp.monitored,
		       sw.book_id, sw.position_in_series, bw.title
		FROM series_books sp
		JOIN books bp ON bp.id = sp.book_id
		JOIN series_books sw ON sw.series_id = sp.series_id AND sw.book_id <> sp.book_id
		JOIN books bw ON bw.id = sw.book_id
		WHERE bp.status <> ?
		  AND instr(sp.position_in_series, '.') > 0
		  AND bw.excluded = 0
		  AND (bw.status = ? OR bw.monitored = 1)
		  AND bw.owner_user_id IS bp.owner_user_id`
	args := []any{models.BookStatusImported, models.BookStatusImported}
	if seriesID > 0 {
		query += ` AND sp.series_id = ?`
		args = append(args, seriesID)
	}
	query += ` ORDER BY sp.series_id, sp.book_id, sw.book_id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list split edition parts: %w", err)
	}
	defer rows.Close()

	type key struct{ series, book int64 }
	seen := make(map[key]struct{})
	var parts []SplitEditionPart
	for rows.Next() {
		var p SplitEditionPart
		var partPos, wholePos string
		var monitored int
		if err := rows.Scan(&p.SeriesID, &p.BookID, &partPos, &p.Title, &monitored,
			&p.WholeBookID, &wholePos, &p.WholeTitle); err != nil {
			return nil, fmt.Errorf("scan split edition part: %w", err)
		}
		if _, dup := seen[key{p.SeriesID, p.BookID}]; dup {
			continue
		}
		if !seriesmatch.SplitEditionPartOf(p.Title, partPos, p.WholeTitle, wholePos) {
			continue
		}
		p.Monitored = monitored == 1
		seen[key{p.SeriesID, p.BookID}] = struct{}{}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

// UnmonitorNotImported clears the monitored flag on the given books, skipping
// any that are already imported, and reports how many rows changed. Nothing is
// deleted: the books stay in the library and can be monitored again by hand.
func (r *BookRepo) UnmonitorNotImported(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, timeValueArg(time.Now().UTC()), models.BookStatusImported)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	//nolint:gosec // placeholders are a fixed list of "?" markers; every value is bound via args
	res, err := r.db.ExecContext(ctx,
		`UPDATE books SET monitored = 0, updated_at = ?
		 WHERE monitored = 1 AND status <> ? AND id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("unmonitor books: %w", err)
	}
	return res.RowsAffected()
}
