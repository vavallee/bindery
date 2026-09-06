package db

import (
	"context"
	"fmt"
	"time"
)

// Per-indexer query accounting for the daily cap (#2312).
//
// Counts are bucketed by hour rather than kept as one running total so the
// window can roll: usage is the sum of the buckets inside the last 24 hours, and
// capacity comes back gradually as old buckets age out instead of the whole
// allowance unlocking at one instant.
//
// None of these touch indexers.updated_at. The rate-limit cooldown (#1934) reads
// a newer updated_at as "the user edited this row, try again" and drops the
// cooldown, so a counter written on a schedule nobody chose would keep undoing
// it. This is the same rule RecordSearchFailure documents.

// TruncateQueryHour returns the bucket t belongs to. Exported so the searcher and
// its tests agree with the store on where an hour starts.
func TruncateQueryHour(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

// LoadQueryCounts returns every stored bucket at or after since, keyed by
// indexer id and then by bucket start. Callers hold the result in memory and
// keep counting from it, so this is read once rather than per search.
func (r *IndexerRepo) LoadQueryCounts(ctx context.Context, since time.Time) (map[int64]map[time.Time]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT indexer_id, hour_start, count
		FROM indexer_query_counts
		WHERE hour_start >= ?`, TruncateQueryHour(since))
	if err != nil {
		return nil, fmt.Errorf("load indexer query counts: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]map[time.Time]int)
	for rows.Next() {
		var id int64
		var hour time.Time
		var count int
		if err := rows.Scan(&id, &hour, &count); err != nil {
			return nil, fmt.Errorf("scan indexer query count: %w", err)
		}
		if out[id] == nil {
			out[id] = make(map[time.Time]int)
		}
		out[id][hour.UTC()] = count
	}
	return out, rows.Err()
}

// AddQueryCount adds delta requests to one indexer's hour bucket.
//
// Additive rather than absolute, and the caller passes only what it has not
// written yet. Writing the whole in-memory tally would mean any process that
// failed to read the stored buckets first would overwrite them with its own
// small number, destroying counts an earlier run had legitimately recorded. A
// delta cannot do that: it is what this process observed, whatever else is
// already in the row.
func (r *IndexerRepo) AddQueryCount(ctx context.Context, id int64, hourStart time.Time, delta int) error {
	if delta <= 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO indexer_query_counts (indexer_id, hour_start, count)
		VALUES (?, ?, ?)
		ON CONFLICT(indexer_id, hour_start) DO UPDATE SET count=count+excluded.count`,
		id, TruncateQueryHour(hourStart), delta)
	if err != nil {
		return fmt.Errorf("record indexer query count: %w", err)
	}
	return nil
}

// PruneQueryCounts drops buckets that have fallen out of the window. Without it
// the table would grow one row per indexer per hour forever; with it an indexer
// never holds more than the handful of buckets the window can see.
func (r *IndexerRepo) PruneQueryCounts(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM indexer_query_counts WHERE hour_start < ?", TruncateQueryHour(before))
	if err != nil {
		return fmt.Errorf("prune indexer query counts: %w", err)
	}
	return nil
}

// QueryUsage returns how many requests each indexer has been sent since the
// given time, for the API to render against the configured cap. Indexers with no
// stored buckets are absent from the map rather than present with a zero.
func (r *IndexerRepo) QueryUsage(ctx context.Context, since time.Time) (map[int64]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT indexer_id, COALESCE(SUM(count), 0)
		FROM indexer_query_counts
		WHERE hour_start >= ?
		GROUP BY indexer_id`, TruncateQueryHour(since))
	if err != nil {
		return nil, fmt.Errorf("sum indexer query counts: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]int)
	for rows.Next() {
		var id, total int64
		if err := rows.Scan(&id, &total); err != nil {
			return nil, fmt.Errorf("scan indexer query usage: %w", err)
		}
		out[id] = int(total)
	}
	return out, rows.Err()
}
