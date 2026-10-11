package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrSeriesMergeInvalid reports a merge request that cannot be carried out as
// asked: no sources, the target among them, a duplicate or a missing source.
var ErrSeriesMergeInvalid = errors.New("invalid series merge")

// SeriesMergePlan is what merging Sources into the target series does. A dry
// run returns it unapplied; Merge applies it in one transaction.
type SeriesMergePlan struct {
	TargetID int64  `json:"targetId"`
	Title    string `json:"title"` // the target's title after the merge
	// Aliases are the provider foreign ids that resolve to the target after
	// the merge: the sources' own ids and the aliases they already had.
	Aliases []string            `json:"aliases"`
	Sources []SeriesMergeSource `json:"sources"`
	// HardcoverLinkFrom is the source whose Hardcover link the target takes,
	// 0 when the target keeps its own or none of them has one. The same for
	// GenreOverrideFrom.
	HardcoverLinkFrom int64 `json:"hardcoverLinkFrom"`
	GenreOverrideFrom int64 `json:"genreOverrideFrom"`
	// Monitored is the target's monitored state after the merge: monitored
	// when it or any source was.
	Monitored bool `json:"monitored"`
}

// SeriesMergeSource is one series merged into the target.
type SeriesMergeSource struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	ForeignID string `json:"foreignSeriesId"`
	// Moved are books that join the target; Kept are books already in it.
	Moved []SeriesMergeBook `json:"moved"`
	Kept  []SeriesMergeBook `json:"kept"`
	// Conflicts are books in both series with different positions; the
	// target's position stays.
	Conflicts []SeriesMergeConflict `json:"conflicts"`
}

// SeriesMergeBook is a book and the position it has in the target after the
// merge, and whether the target becomes its primary series.
type SeriesMergeBook struct {
	BookID   int64  `json:"bookId"`
	Title    string `json:"title"`
	Position string `json:"position"`
	Primary  bool   `json:"primary"`
}

// SeriesMergeConflict is a book whose position differs between the target
// and a source.
type SeriesMergeConflict struct {
	BookID         int64  `json:"bookId"`
	Title          string `json:"title"`
	TargetPosition string `json:"targetPosition"`
	SourcePosition string `json:"sourcePosition"`
}

type mergeSeriesRow struct {
	id            int64
	foreignID     string
	title         string
	monitored     bool
	genreOverride sql.NullString
	hardcoverLink bool
	aliases       []string
}

type mergeMember struct {
	bookID   int64
	title    string
	position string
	primary  bool
}

// PlanMerge computes what merging sourceIDs into targetID would do, without
// changing anything. newTitle, when not blank, renames the target.
func (r *SeriesRepo) PlanMerge(ctx context.Context, targetID int64, sourceIDs []int64, newTitle string) (*SeriesMergePlan, error) {
	return r.planMerge(ctx, targetID, sourceIDs, newTitle)
}

// Merge merges sourceIDs into targetID (#2554) in one transaction and returns
// what it did. Each source's books join the target; where a book is in both,
// the target's row stays, its position wins when both are set and the source
// fills an empty one, and the target becomes the book's primary series when
// the source was (still one primary per book, #2525). Each source's foreign
// id, and every alias it had, then resolves to the target, so a refresh that
// still reports a source id links to the target instead of recreating the
// source. The target keeps its own Hardcover link and genre override and
// takes a source's only when it has none; it is monitored when any of the
// series was. The sources are deleted.
//
// Import provenance (ABS, Calibre) is not rewritten: rolling back an import
// made before the merge leaves the books it filed in the target.
func (r *SeriesRepo) Merge(ctx context.Context, targetID int64, sourceIDs []int64, newTitle string) (*SeriesMergePlan, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("merge series: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txr := r.WithTx(tx)

	plan, err := txr.planMerge(ctx, targetID, sourceIDs, newTitle)
	if err != nil {
		return nil, err
	}
	if err := txr.applyMerge(ctx, plan); err != nil {
		return nil, fmt.Errorf("merge series into %d: %w", targetID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("merge series: %w", err)
	}
	return plan, nil
}

// planMerge reads the series involved through r.exec, so inside Merge the
// plan and its application see the same state.
func (r *SeriesRepo) planMerge(ctx context.Context, targetID int64, sourceIDs []int64, newTitle string) (*SeriesMergePlan, error) {
	if len(sourceIDs) == 0 {
		return nil, fmt.Errorf("%w: no series to merge", ErrSeriesMergeInvalid)
	}
	seen := map[int64]bool{}
	for _, id := range sourceIDs {
		switch {
		case id == targetID:
			return nil, fmt.Errorf("%w: a series cannot be merged into itself", ErrSeriesMergeInvalid)
		case seen[id]:
			return nil, fmt.Errorf("%w: series %d listed twice", ErrSeriesMergeInvalid, id)
		}
		seen[id] = true
	}
	target, err := r.mergeRow(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, sql.ErrNoRows
	}
	targetMembers, err := r.mergeMembers(ctx, targetID)
	if err != nil {
		return nil, err
	}

	plan := &SeriesMergePlan{TargetID: targetID, Title: target.title, Monitored: target.monitored, Aliases: []string{}}
	if t := strings.TrimSpace(newTitle); t != "" {
		plan.Title = t
	}
	hasLink, hasGenres := target.hardcoverLink, target.genreOverride.Valid

	// in tracks the target's membership as the plan builds up, so a book in
	// two sources is moved once and then kept.
	in := map[int64]mergeMember{}
	for _, m := range targetMembers {
		in[m.bookID] = m
	}
	ordered := append([]int64(nil), sourceIDs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		src, err := r.mergeRow(ctx, id)
		if err != nil {
			return nil, err
		}
		if src == nil {
			return nil, fmt.Errorf("%w: series %d does not exist", ErrSeriesMergeInvalid, id)
		}
		srcMembers, err := r.mergeMembers(ctx, id)
		if err != nil {
			return nil, err
		}
		ps := SeriesMergeSource{ID: id, Title: src.title, ForeignID: src.foreignID,
			Moved: []SeriesMergeBook{}, Kept: []SeriesMergeBook{}, Conflicts: []SeriesMergeConflict{}}
		for _, m := range srcMembers {
			have, ok := in[m.bookID]
			if !ok {
				in[m.bookID] = m
				ps.Moved = append(ps.Moved, SeriesMergeBook{BookID: m.bookID, Title: m.title, Position: m.position, Primary: m.primary})
				continue
			}
			pos := have.position
			if pos == "" {
				pos = m.position
			} else if m.position != "" && m.position != have.position {
				ps.Conflicts = append(ps.Conflicts, SeriesMergeConflict{BookID: m.bookID, Title: m.title, TargetPosition: have.position, SourcePosition: m.position})
			}
			kept := mergeMember{bookID: m.bookID, title: have.title, position: pos, primary: have.primary || m.primary}
			in[m.bookID] = kept
			ps.Kept = append(ps.Kept, SeriesMergeBook{BookID: m.bookID, Title: kept.title, Position: pos, Primary: kept.primary})
		}
		plan.Sources = append(plan.Sources, ps)
		if keepAsAlias(src.foreignID) {
			plan.Aliases = append(plan.Aliases, src.foreignID)
		}
		plan.Aliases = append(plan.Aliases, src.aliases...)
		if !hasLink && src.hardcoverLink {
			plan.HardcoverLinkFrom, hasLink = id, true
		}
		if !hasGenres && src.genreOverride.Valid {
			plan.GenreOverrideFrom, hasGenres = id, true
		}
		plan.Monitored = plan.Monitored || src.monitored
	}
	return plan, nil
}

func (r *SeriesRepo) mergeRow(ctx context.Context, id int64) (*mergeSeriesRow, error) {
	var s mergeSeriesRow
	var monitored int
	var aliases sql.NullString
	// One query, aliases included: they come back joined by the unit
	// separator, which no foreign id contains.
	err := r.exec.QueryRowContext(ctx, `
		SELECT s.id, s.foreign_id, s.title, s.monitored, s.genre_override,
		       EXISTS (SELECT 1 FROM series_hardcover_links l WHERE l.series_id = s.id),
		       (SELECT group_concat(foreign_id, char(31)) FROM (SELECT foreign_id FROM series_aliases WHERE series_id = s.id ORDER BY foreign_id))
		FROM series s WHERE s.id = ?`, id).Scan(&s.id, &s.foreignID, &s.title, &monitored, &s.genreOverride, &s.hardcoverLink, &aliases)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read series %d: %w", id, err)
	}
	s.monitored = monitored == 1
	if aliases.Valid && aliases.String != "" {
		s.aliases = strings.Split(aliases.String, "\x1f")
	}
	return &s, nil
}

// mergeMembers lists every book in a series, whoever owns it: a merge is an
// admin action on the shared series rows, so it moves memberships the caller
// cannot see too.
func (r *SeriesRepo) mergeMembers(ctx context.Context, seriesID int64) ([]mergeMember, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT sb.book_id, COALESCE(b.title, ''), sb.position_in_series, sb.primary_series
		FROM series_books sb LEFT JOIN books b ON b.id = sb.book_id
		WHERE sb.series_id = ? ORDER BY sb.book_id`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("read series %d books: %w", seriesID, err)
	}
	defer rows.Close()
	var out []mergeMember
	for rows.Next() {
		var m mergeMember
		var primary int
		if err := rows.Scan(&m.bookID, &m.title, &m.position, &primary); err != nil {
			return nil, err
		}
		m.position = strings.TrimSpace(m.position)
		m.primary = primary == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// mergeStep is one statement of a merge; what names it in an error.
type mergeStep struct {
	what  string
	query string
	args  []any
}

func (r *SeriesRepo) applyMerge(ctx context.Context, plan *SeriesMergePlan) error {
	t := plan.TargetID
	var steps []mergeStep
	add := func(what, query string, args ...any) { steps = append(steps, mergeStep{what, query, args}) }
	for _, src := range plan.Sources {
		for _, b := range src.Moved {
			add("move a book", `INSERT INTO series_books (series_id, book_id, position_in_series, primary_series) VALUES (?, ?, ?, ?)`,
				t, b.BookID, b.Position, boolToInt(b.Primary))
		}
		for _, b := range src.Kept {
			add("update a book", `UPDATE series_books SET position_in_series = ?, primary_series = ? WHERE series_id = ? AND book_id = ?`,
				b.Position, boolToInt(b.Primary), t, b.BookID)
		}
		if plan.HardcoverLinkFrom == src.ID {
			add("move the hardcover link", `UPDATE series_hardcover_links SET series_id = ? WHERE series_id = ?`, t, src.ID)
		}
		if plan.GenreOverrideFrom == src.ID {
			add("copy the genre override", `UPDATE series SET genre_override = (SELECT genre_override FROM series WHERE id = ?) WHERE id = ?`, src.ID, t)
		}
		add("move author series monitoring", `INSERT OR IGNORE INTO author_monitored_series (author_id, series_id, created_at)
			SELECT author_id, ?, created_at FROM author_monitored_series WHERE series_id = ?`, t, src.ID)
		add("repoint recommendations", `UPDATE recommendations SET series_id = ? WHERE series_id = ?`, t, src.ID)
		// Repoint the source's aliases before deleting it, or the cascade
		// drops them; the source's own id becomes an alias once its row is
		// gone, so an alias never equals a live foreign id.
		add("repoint aliases", `UPDATE series_aliases SET series_id = ? WHERE series_id = ?`, t, src.ID)
		add("delete the merged series", `DELETE FROM series WHERE id = ?`, src.ID)
		if keepAsAlias(src.ForeignID) {
			add("alias the merged series' id", `INSERT INTO series_aliases (foreign_id, series_id) VALUES (?, ?)`, src.ForeignID, t)
		} else if src.ForeignID != "" {
			// Nothing resolves the id any more, so what was kept out of it
			// has nothing left to stay out of.
			add("drop the merged series' exclusions", `DELETE FROM book_series_exclusions WHERE series_foreign_id = ?`, src.ForeignID)
		}
	}
	// A book the user took out of one series and that is in the target, or
	// joined it now, is in the series the exclusion resolves to: forget it,
	// or the book page lists it as kept out of a series it is in.
	add("drop exclusions the merge made stale", `DELETE FROM book_series_exclusions
		WHERE book_id IN (SELECT book_id FROM series_books WHERE series_id = ?)
		  AND (series_foreign_id = (SELECT foreign_id FROM series WHERE id = ?)
		       OR series_foreign_id IN (SELECT foreign_id FROM series_aliases WHERE series_id = ?))`, t, t, t)
	add("update the series", `UPDATE series SET title = ?, monitored = ? WHERE id = ?`, plan.Title, boolToInt(plan.Monitored), t)
	for _, st := range steps {
		if _, err := r.exec.ExecContext(ctx, st.query, st.args...); err != nil {
			return fmt.Errorf("%s: %w", st.what, err)
		}
	}
	return nil
}

// keepAsAlias reports whether a merged series' foreign id is worth keeping:
// only a provider's id can come back on a refresh.
func keepAsAlias(foreignID string) bool {
	return foreignID != "" && !strings.HasPrefix(foreignID, manualSeriesPrefix)
}
