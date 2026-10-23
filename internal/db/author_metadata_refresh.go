package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Staggered author profile refresh (#2595).
//
// The profile refresh used to run nightly, walk every author in one burst and
// never read authors.last_metadata_refresh_at. These queries give it a persisted
// cursor on that column: the scheduler asks for the stalest authors, refreshes
// a bounded few, and stamps each one only after its refresh succeeded.

// metadataRefreshEligibleWhere is the author filter shared by the due list and
// the eligible count, so the batch size the scheduler derives from the count
// is always a share of the same set it then walks.
//
// Monitored authors only, as the refresh always was, and not Calibre shells,
// whose synthetic foreign id no provider answers for. Unlike discovery this
// does not drop authors whose monitor_new_items is "none": that setting is
// about adding books, and an author's profile is worth keeping fresh either way.
const metadataRefreshEligibleWhere = `monitored = 1 AND foreign_id NOT LIKE 'calibre:%'`

// ListMetadataRefreshDue returns up to limit eligible authors whose profile was
// last refreshed before cutoff, never refreshed authors first and then the
// oldest refresh first, ties broken by id.
//
// The column has been written by several paths over the releases (the full
// row Update, the ABS import, older builds), so the comparison and the
// ordering are done on parsed times, not on text. The eligible set read is
// one small row per author.
func (r *AuthorRepo) ListMetadataRefreshDue(ctx context.Context, now, cutoff time.Time, limit int) ([]models.Author, error) {
	if limit <= 0 {
		return nil, nil
	}
	due, err := r.metadataRefreshCandidates(ctx, now, cutoff)
	if err != nil {
		return nil, err
	}

	sort.Slice(due, func(i, j int) bool {
		a, b := due[i], due[j]
		switch {
		case a.last == nil && b.last != nil:
			return true
		case a.last != nil && b.last == nil:
			return false
		case a.last != nil && !a.last.Equal(*b.last):
			return a.last.Before(*b.last)
		}
		return a.id < b.id
	})
	if len(due) > limit {
		due = due[:limit]
	}

	out := make([]models.Author, 0, len(due))
	for _, c := range due {
		a, err := r.GetByID(ctx, c.id)
		if err != nil {
			return nil, fmt.Errorf("load author %d due for profile refresh: %w", c.id, err)
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

type metadataRefreshCandidate struct {
	id   int64
	last *time.Time
}

// metadataRefreshCandidates reads the eligible authors whose refresh stamp is
// missing, in the future of now, or older than cutoff, unordered.
func (r *AuthorRepo) metadataRefreshCandidates(ctx context.Context, now, cutoff time.Time) ([]metadataRefreshCandidate, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, last_metadata_refresh_at FROM authors WHERE "+metadataRefreshEligibleWhere)
	if err != nil {
		return nil, fmt.Errorf("list authors due for profile refresh: %w", err)
	}
	defer rows.Close()
	var due []metadataRefreshCandidate
	for rows.Next() {
		var (
			id  int64
			raw sql.NullString
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan author due for profile refresh: %w", err)
		}
		last, perr := parseFlexibleTime(raw)
		if perr != nil {
			// An unreadable stamp must not exempt an author from refresh.
			slog.Warn("unparseable author.last_metadata_refresh_at, treating as never refreshed", "author_id", id, "value", raw.String, "error", perr)
			last = nil
		}
		if last != nil && last.After(now) {
			// A stamp from the future (clock moved back, restored backup)
			// would hide the author until the clock catches up. Treat it as
			// never refreshed.
			last = nil
		}
		if last == nil || last.Before(cutoff) {
			due = append(due, metadataRefreshCandidate{id: id, last: last})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list authors due for profile refresh: %w", err)
	}
	return due, nil
}

// CountMetadataRefreshEligible counts every author the staggered profile
// refresh would ever visit, due or not. The scheduler spreads that many
// authors over a day of ticks.
func (r *AuthorRepo) CountMetadataRefreshEligible(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM authors WHERE "+metadataRefreshEligibleWhere).Scan(&n); err != nil {
		return 0, fmt.Errorf("count authors eligible for profile refresh: %w", err)
	}
	return n, nil
}

// StampMetadataRefresh records that authorID's profile was refreshed at when.
// A single column UPDATE on purpose, like StampDiscovery: the whole row Update
// writes every field from a snapshot and would undo an edit saved since the
// snapshot was read. It leaves updated_at alone, so it is not an edit that
// UpdateIfUnchanged would mistake for a concurrent change.
func (r *AuthorRepo) StampMetadataRefresh(ctx context.Context, authorID int64, when time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE authors SET last_metadata_refresh_at = ? WHERE id = ?`,
		when.UTC().Format(time.RFC3339Nano), authorID); err != nil {
		return fmt.Errorf("stamp metadata refresh for author %d: %w", authorID, err)
	}
	return nil
}
