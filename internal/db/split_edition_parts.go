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
// matched by the title signal alone, seriesmatch.SplitEditionTitleOf (#3324),
// evaluated here as its two halves, SplitEditionBase of the part and
// CleanTitle of the whole, so each title is parsed once. Hardcover can list a
// part only in another numbering of the series, where each part is its own
// volume at an integer position ("The Eye of the World, Part 1 of 2" at 1 in
// The Wheel of Time (Finnish Numbering)). Positions from two series are not
// comparable, so signal 1 cannot be evaluated there. The whole must itself be
// in a series, and no series the part is in may hold a book of the part's
// owner with the whole's title (compared by CleanTitle, as the match is;
// series rows carry no owner, so another user's books there are ignored):
// there only the two signal
// rule decides, so a part numbered as its own volume inside the whole's
// series is left alone even when the library has a second copy of the whole
// elsewhere. A part matched both ways reports the same-series whole.
//
// Only a part that is not imported is reported: an imported part is already on
// disk and there is nothing to stop searching for. The whole has to be a book
// the user actually has or wants, so it must be imported or monitored, and not
// excluded — the whole gate below and api.eligibleSplitEditionWhole
// (internal/api/series.go) apply the identical eligibility rule to the
// catalogue diff; a change to it here needs the same change made there. The
// catalogue diff only pairs within one series and has no counterpart to the
// cross-series match. Both rows must belong to the same owner, so one user's
// library never decides what another user's sweep searches for.
func (r *BookRepo) ListCoveredSplitEditionParts(ctx context.Context, seriesID int64) ([]SplitEditionPart, error) {
	matches, guardIDs, err := r.coveredSplitEditionCandidates(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	shelvedWithWhole, err := r.partsShelvedWithTitle(ctx, guardIDs)
	if err != nil {
		return nil, err
	}

	type key struct{ series, book int64 }
	seen := make(map[key]struct{})
	var parts []SplitEditionPart
	for _, m := range matches {
		k := key{m.SeriesID, m.BookID}
		if _, dup := seen[k]; dup {
			continue
		}
		if _, shelved := shelvedWithWhole[m.BookID]; m.cross && shelved {
			continue
		}
		seen[k] = struct{}{}
		parts = append(parts, m.SplitEditionPart)
	}
	return parts, nil
}

// splitEditionMatch is a (part, whole) row that passed its rule. A cross
// match still has to clear the shared-series guard.
type splitEditionMatch struct {
	SplitEditionPart
	cross bool
}

// coveredSplitEditionCandidates reads the candidate query and keeps the rows
// that pass their rule, part by part with same-series rows first. It returns
// the base title of every cross-matched part, for the guard. The rows are
// closed before it returns, so the guard can query on the same connection.
func (r *BookRepo) coveredSplitEditionCandidates(ctx context.Context, seriesID int64) ([]splitEditionMatch, map[int64]string, error) {
	query, args := coveredSplitEditionPartsQuery(seriesID)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list split edition parts: %w", err)
	}
	defer rows.Close()

	// Cross rows pair one part with many wholes, so each title is parsed once.
	type base struct {
		title string
		ok    bool
	}
	partBases := make(map[int64]base)
	wholeTitles := make(map[int64]string)
	var matches []splitEditionMatch
	guardIDs := make(map[int64]string) // cross-matched part id -> its base title
	for rows.Next() {
		var m splitEditionMatch
		var partPos, wholePos string
		var monitored, sameSeries int
		if err := rows.Scan(&m.SeriesID, &m.BookID, &partPos, &m.Title, &monitored,
			&m.WholeBookID, &wholePos, &m.WholeTitle, &sameSeries); err != nil {
			return nil, nil, fmt.Errorf("scan split edition part: %w", err)
		}
		m.Monitored = monitored == 1
		if sameSeries == 1 {
			if !seriesmatch.SplitEditionPartOf(m.Title, partPos, m.WholeTitle, wholePos) {
				continue
			}
		} else {
			pb, ok := partBases[m.BookID]
			if !ok {
				pb.title, pb.ok = seriesmatch.SplitEditionBase(m.Title)
				partBases[m.BookID] = pb
			}
			if !pb.ok {
				continue
			}
			wt, ok := wholeTitles[m.WholeBookID]
			if !ok {
				wt = seriesmatch.CleanTitle(m.WholeTitle)
				wholeTitles[m.WholeBookID] = wt
			}
			if pb.title != wt {
				continue
			}
			m.cross = true
			guardIDs[m.BookID] = pb.title
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list split edition parts: %w", err)
	}
	return matches, guardIDs, nil
}

// coveredSplitEditionPartsQuery builds the candidate query behind
// ListCoveredSplitEditionParts: every (part, whole) row that passes the SQL
// side of either rule, same-series rows first for each part.
func coveredSplitEditionPartsQuery(seriesID int64) (string, []any) {
	// The unary + keeps idx_books_owner out of both branches. Given the
	// choice, SQLite reached the whole through it, which on a single user
	// library is every book: every book against every book in the cross
	// series branch (about 30 s on 10k books), and every book per series row
	// in the same-series branch when there is a series filter.
	const wholeGate = `
		  AND bw.excluded = 0
		  AND (bw.status = ? OR bw.monitored = 1)
		  AND +bw.owner_user_id IS bp.owner_user_id`
	// CROSS JOIN pins the order of the cross-series branch: start from the
	// parts (from the one series' rows when there is a series filter), then
	// reach the author's other books by idx_books_author_id.
	crossFrom := `
		FROM books bp
		CROSS JOIN series_books sp ON sp.book_id = bp.id`
	seriesFilter := ""
	if seriesID > 0 {
		crossFrom = `
		FROM series_books sp
		CROSS JOIN books bp ON bp.id = sp.book_id`
		seriesFilter = ` AND sp.series_id = ?`
	}
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
		       bw.id, '', bw.title, 0` + crossFrom + `
		CROSS JOIN books bw ON bw.author_id = bp.author_id AND bw.id <> bp.id
		WHERE bp.status <> ?
		  AND (bp.title GLOB '*[Pp][Aa][Rr][Tt]*[0-9]*' OR bp.title GLOB '*[Pp][Tt]*[0-9]*')` + wholeGate + `
		  AND EXISTS (SELECT 1 FROM series_books ws WHERE ws.book_id = bw.id)` + seriesFilter + `
		)
		ORDER BY part_series, part_id, same_series DESC, whole_id`
	// Both branches bind the same placeholders in the same order.
	branchArgs := []any{models.BookStatusImported, models.BookStatusImported}
	if seriesID > 0 {
		branchArgs = append(branchArgs, seriesID)
	}
	return query, append(append([]any{}, branchArgs...), branchArgs...)
}

// partsShelvedWithTitle returns the parts among bases (part id -> base title)
// that share a series with another book of the same owner whose CleanTitle is
// that base title, which is the cross-series rule's guard. The comparison is
// done here, in Go, so it normalises titles exactly as the match itself does.
func (r *BookRepo) partsShelvedWithTitle(ctx context.Context, bases map[int64]string) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	if len(bases) == 0 {
		return out, nil
	}
	list := make([]int64, 0, len(bases))
	for id := range bases {
		list = append(list, id)
	}
	const chunk = 500
	for len(list) > 0 {
		n := min(chunk, len(list))
		batch := list[:n]
		list = list[n:]
		if err := r.markShelvedWithTitle(ctx, batch, bases, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// markShelvedWithTitle adds to out each part in batch that shares a series
// with another book of the same owner whose CleanTitle is the part's base.
func (r *BookRepo) markShelvedWithTitle(ctx context.Context, batch []int64, bases map[int64]string, out map[int64]struct{}) error {
	placeholders := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, id := range batch {
		placeholders[i] = "?"
		args[i] = id
	}
	//nolint:gosec // placeholders are a fixed list of "?" markers; every value is bound via args
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT x.book_id, b2.title
		FROM series_books x
		JOIN books bx ON bx.id = x.book_id
		JOIN series_books y ON y.series_id = x.series_id AND y.book_id <> x.book_id
		JOIN books b2 ON b2.id = y.book_id
		WHERE x.book_id IN (`+strings.Join(placeholders, ",")+`)
		  AND b2.owner_user_id IS bx.owner_user_id`, args...)
	if err != nil {
		return fmt.Errorf("list split edition part neighbours: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var partID int64
		var title string
		if err := rows.Scan(&partID, &title); err != nil {
			return fmt.Errorf("scan split edition part neighbour: %w", err)
		}
		if seriesmatch.CleanTitle(title) == bases[partID] {
			out[partID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list split edition part neighbours: %w", err)
	}
	return nil
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
