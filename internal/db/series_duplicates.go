package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/vavallee/bindery/internal/seriesmatch"
	"github.com/vavallee/bindery/internal/textutil"
)

// TitleDuplicateSeries is one series in a TitleDuplicateGroup.
type TitleDuplicateSeries struct {
	ID              int64    `json:"id"`
	Title           string   `json:"title"`
	ForeignID       string   `json:"foreignSeriesId"`
	BookCount       int      `json:"bookCount"`
	Authors         []string `json:"authors"`
	HardcoverLinked bool     `json:"hardcoverLinked"`
	Monitored       bool     `json:"monitored"`
}

// TitleDuplicateGroup is two or more series whose titles share the key the
// Audiobookshelf import looks a series up by (seriesmatch.LookupKey), so it
// cannot tell them apart (#2173).
type TitleDuplicateGroup struct {
	Key string `json:"key"`
	// SuggestedKeepID is the series to merge the others into: the one with a
	// Hardcover link, else the one with the most books, else the lowest id.
	SuggestedKeepID int64 `json:"suggestedKeepId"`
	// SameAuthors is true when every series in the group that holds books
	// shares at least one author with the suggested one. Empty series count
	// as compatible. False means the titles probably name two different
	// series, and the group should only be merged by hand.
	SameAuthors bool `json:"sameAuthors"`
	// Series lists the suggested keep first, then the rest by id.
	Series []TitleDuplicateSeries `json:"series"`
}

// ListTitleDuplicates groups every series by seriesmatch.LookupKey and
// returns the groups with more than one member. Series rows are global and
// this is an admin view, so books of every owner count. Two queries in all,
// however large the library: one for the series and one for their authors.
func (r *SeriesRepo) ListTitleDuplicates(ctx context.Context) ([]TitleDuplicateGroup, error) {
	byKey, err := r.seriesByLookupKey(ctx)
	if err != nil {
		return nil, err
	}

	inGroup := map[int64]*TitleDuplicateSeries{}
	for _, members := range byKey {
		if len(members) < 2 {
			continue
		}
		for _, s := range members {
			inGroup[s.ID] = s
		}
	}
	if len(inGroup) == 0 {
		return []TitleDuplicateGroup{}, nil
	}

	// Every author of every series book, in one pass. Only the series in a
	// group are kept, which is cheaper than binding thousands of ids.
	arows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT sb.series_id, a.name
		FROM series_books sb
		JOIN books b ON b.id = sb.book_id
		JOIN authors a ON a.id = b.author_id
		ORDER BY sb.series_id, a.name`)
	if err != nil {
		return nil, fmt.Errorf("list series authors for duplicates: %w", err)
	}
	defer arows.Close()
	authorKeys := map[int64]map[string]bool{}
	for arows.Next() {
		var id int64
		var name string
		if err := arows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan series author for duplicates: %w", err)
		}
		s, ok := inGroup[id]
		if !ok {
			continue
		}
		// Each account can hold its own row for one author (#3555), so the
		// same person can arrive twice under two ids: compare and list by
		// normalized name.
		key := textutil.NormalizeAuthorName(name)
		if key == "" {
			continue
		}
		if authorKeys[id] == nil {
			authorKeys[id] = map[string]bool{}
		}
		if authorKeys[id][key] {
			continue
		}
		authorKeys[id][key] = true
		s.Authors = append(s.Authors, name)
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}

	groups := make([]TitleDuplicateGroup, 0)
	for key, members := range byKey {
		if len(members) < 2 {
			continue
		}
		groups = append(groups, buildTitleDuplicateGroup(key, members, authorKeys))
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Key != groups[j].Key {
			return groups[i].Key < groups[j].Key
		}
		return groups[i].SuggestedKeepID < groups[j].SuggestedKeepID
	})
	return groups, nil
}

// seriesByLookupKey reads every series once and buckets it by
// seriesmatch.LookupKey. The rows are closed before it returns, which the
// author query after it needs on a one connection pool.
func (r *SeriesRepo) seriesByLookupKey(ctx context.Context) (map[string][]*TitleDuplicateSeries, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.title, s.foreign_id, s.monitored,
		       EXISTS (SELECT 1 FROM series_hardcover_links l WHERE l.series_id = s.id),
		       (SELECT COUNT(*) FROM series_books sb WHERE sb.series_id = s.id)
		FROM series s ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("list series for duplicates: %w", err)
	}
	defer rows.Close()
	byKey := map[string][]*TitleDuplicateSeries{}
	for rows.Next() {
		var s TitleDuplicateSeries
		var monitored int
		if err := rows.Scan(&s.ID, &s.Title, &s.ForeignID, &monitored, &s.HardcoverLinked, &s.BookCount); err != nil {
			return nil, fmt.Errorf("scan series for duplicates: %w", err)
		}
		s.Monitored = monitored == 1
		s.Authors = []string{}
		key := seriesmatch.LookupKey(s.Title)
		if key == "" {
			// A title that normalizes to nothing matches nothing on lookup.
			continue
		}
		byKey[key] = append(byKey[key], &s)
	}
	return byKey, rows.Err()
}

func buildTitleDuplicateGroup(key string, members []*TitleDuplicateSeries, authorKeys map[int64]map[string]bool) TitleDuplicateGroup {
	keep := members[0]
	for _, s := range members[1:] {
		if betterKeep(s, keep) {
			keep = s
		}
	}
	// The authors every other series is compared with: the keep's, or, when
	// the keep holds no books (a linked but empty series), those of the
	// fullest series, so two unrelated series do not pass through an empty
	// one.
	ref := keep
	if ref.BookCount == 0 {
		for _, s := range members {
			if s.BookCount > ref.BookCount || (s.BookCount == ref.BookCount && s.BookCount > 0 && s.ID < ref.ID) {
				ref = s
			}
		}
	}
	same := true
	for _, s := range members {
		if s == ref || s.BookCount == 0 {
			continue
		}
		if !sharesAuthor(authorKeys[s.ID], authorKeys[ref.ID]) {
			same = false
			break
		}
	}
	g := TitleDuplicateGroup{Key: key, SuggestedKeepID: keep.ID, SameAuthors: same,
		Series: make([]TitleDuplicateSeries, 0, len(members))}
	g.Series = append(g.Series, *keep)
	for _, s := range members {
		if s != keep {
			g.Series = append(g.Series, *s)
		}
	}
	return g
}

// betterKeep reports whether a is a better series to keep than b.
func betterKeep(a, b *TitleDuplicateSeries) bool {
	if a.HardcoverLinked != b.HardcoverLinked {
		return a.HardcoverLinked
	}
	if a.BookCount != b.BookCount {
		return a.BookCount > b.BookCount
	}
	return a.ID < b.ID
}

// sharesAuthor reports whether two author sets meet. A series whose books
// have no author at all has nothing to disagree with, so it shares.
func sharesAuthor(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// SeriesTitle returns a series' title, and false when there is no such series.
func (r *SeriesRepo) SeriesTitle(ctx context.Context, id int64) (string, bool, error) {
	var title string
	err := r.db.QueryRowContext(ctx, `SELECT title FROM series WHERE id = ?`, id).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get series %d title: %w", id, err)
	}
	return title, true, nil
}
