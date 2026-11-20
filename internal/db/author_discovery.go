package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// discoveryTimeLayout is the fixed width, second precision UTC form the
// discovery cursor is written and compared in. RFC3339Nano trims trailing
// zeros, which breaks string ordering ("10:00:00Z" sorts after
// "10:00:00.5Z"), and the due query compares the column as text so it can
// order NULLs first without a function call per row.
const discoveryTimeLayout = "2006-01-02T15:04:05Z"

// discoveryEligibleWhere is the author filter shared by the due list and the
// eligible count, so the batch size the scheduler derives from the count is
// always a share of the same set it then walks.
//
// Monitored authors only, and not those whose monitor_new_items is "none":
// that setting means "do not add new books from this author", which is the
// only thing a discovery run exists to do. Calibre shells and
// hand-made authors (#3840) carry a synthetic foreign id no provider answers
// for.
const discoveryEligibleWhere = `monitored = 1
	  AND COALESCE(monitor_new_items, 'all') != 'none'
	  AND foreign_id NOT LIKE 'calibre:%'
	  AND foreign_id NOT LIKE 'manual:%'`

// ListDiscoveryDue returns up to limit authors eligible for scheduled release
// discovery whose last check is older than cutoff, never checked authors
// first and then the oldest check first.
func (r *AuthorRepo) ListDiscoveryDue(ctx context.Context, cutoff time.Time, limit int) ([]models.Author, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+authorSelectCols+" FROM authors WHERE "+discoveryEligibleWhere+`
		  AND (last_discovery_at IS NULL OR last_discovery_at < ?)
		ORDER BY last_discovery_at IS NOT NULL, last_discovery_at, id
		LIMIT ?`,
		cutoff.UTC().Format(discoveryTimeLayout), limit)
	if err != nil {
		return nil, fmt.Errorf("list authors due for discovery: %w", err)
	}
	defer rows.Close()
	var out []models.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, fmt.Errorf("scan author due for discovery: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountDiscoveryEligible counts every author scheduled discovery would ever
// visit, due or not. The scheduler spreads that many authors over the hours
// in its interval.
func (r *AuthorRepo) CountDiscoveryEligible(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM authors WHERE "+discoveryEligibleWhere).Scan(&n); err != nil {
		return 0, fmt.Errorf("count authors eligible for discovery: %w", err)
	}
	return n, nil
}

// StampDiscovery records that authorID's catalogue was checked at when. A
// single column UPDATE on purpose: the whole row Update writes every field
// from a snapshot, and a discovery pass holding a snapshot for minutes would
// undo whatever the user edited on that author in the meantime.
func (r *AuthorRepo) StampDiscovery(ctx context.Context, authorID int64, when time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE authors SET last_discovery_at = ? WHERE id = ?`,
		when.UTC().Format(discoveryTimeLayout), authorID); err != nil {
		return fmt.Errorf("stamp discovery for author %d: %w", authorID, err)
	}
	return nil
}

// LastDiscoveryAt reads the discovery cursor for authorID, nil when the author
// has never been checked or does not exist.
func (r *AuthorRepo) LastDiscoveryAt(ctx context.Context, authorID int64) (*time.Time, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT last_discovery_at FROM authors WHERE id = ?`, authorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read last_discovery_at for author %d: %w", authorID, err)
	}
	return parseFlexibleTime(raw)
}
