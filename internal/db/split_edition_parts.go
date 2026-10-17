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
// another book (#3048): "The Way of Kings, Part 1" at 1.1 while "The Way of
// Kings" itself sits at 1. SeriesID is the part's series; the whole may sit in
// another series by the same author (#3324).
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
// A part and a whole that share a series are matched by
// seriesmatch.SplitEditionPartOf, the same two signals the series diff uses
// for catalogue rows (#2524): a fractional position under the whole's integer
// position, and the whole's title plus a "Part N" marker. Here it is applied
// to rows already stored, which is what an install that pressed Fill before
// #2524 has.
//
// A part and a whole that share no series, but have the same author, are
// matched by the title signal alone, seriesmatch.SplitEditionTitleOf (#3324).
// Hardcover can list a part only in another numbering of the series, where
// each part is its own volume at an integer position ("The Eye of the World,
// Part 1 of 2" at 1 in The Wheel of Time (Finnish Numbering)). Positions from
// two series are not comparable, so signal 1 cannot be evaluated there. The
// whole must itself be in a series, and the part's own series must not hold
// any book with the whole's title: there only the two signal rule decides, so
// a part numbered as its own volume inside the whole's series is left alone
// even when the library has a second copy of the whole elsewhere. A part
// matched both ways reports the same-series whole.
//
// Only a part that is not imported is reported: an imported part is already on
// disk and there is nothing to stop searching for. The whole has to be a book
// the user actually has or wants, so it must be imported or monitored, and not
// excluded — the whole gate below and api.eligibleSplitEditionWhole
// (internal/api/series.go) apply the identical eligibility rule to the
// catalogue diff; a change to it here needs the same change made there. The
// catalogue diff only pairs within one series and has no counterpart to the
// cross-series match. Both rows
// must belong to the same owner, so one user's library never decides what
// another user's sweep searches for.
func (r *BookRepo) ListCoveredSplitEditionParts(ctx context.Context, seriesID int64) ([]SplitEditionPart, error) {
	seriesFilter := ""
	if seriesID > 0 {
		seriesFilter = ` AND sp.series_id = ?`
	}
	const wholeGate = `
		  AND bw.excluded = 0
		  AND (bw.status = ? OR bw.monitored = 1)
		  AND bw.owner_user_id IS bp.owner_user_id`
	query := `
		SELECT part_series, part_id, part_pos, part_title, part_monitored,
		       whole_id, whole_pos, whole_title, same_series
		FROM (
		SELECT sp.series_id AS part_series, sp.book_id AS part_id, sp.position_in_series AS part_pos,
		       bp.title AS part_title, bp.monitored AS part_monitored,
		       sw.book_id AS whole_id, sw.position_in_series AS whole_pos, bw.title AS whole_title,
		       1 AS same_series
		FROM series_books sp
		JOIN books bp ON bp.id = sp.book_id
		JOIN series_books sw ON sw.series_id = sp.series_id AND sw.book_id <> sp.book_id
		JOIN books bw ON bw.id = sw.book_id
		WHERE bp.status <> ?
		  AND instr(sp.position_in_series, '.') > 0` + wholeGate + seriesFilter + `
		UNION ALL
		SELECT sp.series_id, sp.book_id, sp.position_in_series, bp.title, bp.monitored,
		       bw.id, '', bw.title, 0
		FROM series_books sp
		JOIN books bp ON bp.id = sp.book_id
		JOIN books bw ON bw.author_id = bp.author_id AND bw.id <> bp.id
		WHERE bp.status <> ?
		  AND (bp.title GLOB '*[Pp][Aa][Rr][Tt]*[0-9]*' OR bp.title GLOB '*[Pp][Tt]*[0-9]*')` + wholeGate + `
		  AND EXISTS (SELECT 1 FROM series_books ws WHERE ws.book_id = bw.id)
		  AND NOT EXISTS (
		      SELECT 1 FROM series_books x
		      JOIN series_books y ON y.series_id = x.series_id AND y.book_id <> x.book_id
		      JOIN books b2 ON b2.id = y.book_id
		      WHERE x.book_id = bp.id AND lower(b2.title) = lower(bw.title))` + seriesFilter + `
		)
		ORDER BY part_series, part_id, same_series DESC, whole_id`
	// Both branches bind the same placeholders in the same order.
	branchArgs := []any{models.BookStatusImported, models.BookStatusImported}
	if seriesID > 0 {
		branchArgs = append(branchArgs, seriesID)
	}
	args := append(append([]any{}, branchArgs...), branchArgs...)

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
		var monitored, sameSeries int
		if err := rows.Scan(&p.SeriesID, &p.BookID, &partPos, &p.Title, &monitored,
			&p.WholeBookID, &wholePos, &p.WholeTitle, &sameSeries); err != nil {
			return nil, fmt.Errorf("scan split edition part: %w", err)
		}
		if _, dup := seen[key{p.SeriesID, p.BookID}]; dup {
			continue
		}
		if sameSeries == 1 {
			if !seriesmatch.SplitEditionPartOf(p.Title, partPos, p.WholeTitle, wholePos) {
				continue
			}
		} else if !seriesmatch.SplitEditionTitleOf(p.Title, p.WholeTitle) {
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
