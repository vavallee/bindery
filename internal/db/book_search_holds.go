package db

import (
	"context"
	"fmt"
	"time"
)

// SetSearchHold keeps a book out of the scheduled wanted search until until
// (#2523), which is the Upcoming state. It never blocks a manual search.
func (r *BookRepo) SetSearchHold(ctx context.Context, bookID int64, until time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO book_search_holds (book_id, until_at) VALUES (?, ?)
		ON CONFLICT(book_id) DO UPDATE SET until_at = excluded.until_at`,
		bookID, until.UTC().Format(discoveryTimeLayout))
	if err != nil {
		return fmt.Errorf("set search hold for book %d: %w", bookID, err)
	}
	return nil
}

// ClearSearchHold lifts the hold on a book, if it has one.
func (r *BookRepo) ClearSearchHold(ctx context.Context, bookID int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM book_search_holds WHERE book_id = ?`, bookID); err != nil {
		return fmt.Errorf("clear search hold for book %d: %w", bookID, err)
	}
	return nil
}

// SearchHolds maps every book still held at now to the date its hold ends.
// A hold whose date has passed is not returned: the book is eligible for the
// normal search again with no state change.
func (r *BookRepo) SearchHolds(ctx context.Context, now time.Time) (map[int64]time.Time, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT book_id, until_at FROM book_search_holds WHERE until_at > ?`, now.UTC().Format(discoveryTimeLayout))
	if err != nil {
		return nil, fmt.Errorf("list search holds: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]time.Time)
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		t, err := time.Parse(discoveryTimeLayout, raw)
		if err != nil {
			continue
		}
		out[id] = t
	}
	return out, rows.Err()
}
