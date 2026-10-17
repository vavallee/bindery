package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StampEnrichment records that the author enrichers gave a full answer for
// authorID at when (#3287). A single column UPDATE, like StampDiscovery, so a
// refresh holding a snapshot never undoes an edit made meanwhile.
func (r *AuthorRepo) StampEnrichment(ctx context.Context, authorID int64, when time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE authors SET last_enrichment_at = ? WHERE id = ?`,
		when.UTC().Format(discoveryTimeLayout), authorID); err != nil {
		return fmt.Errorf("stamp enrichment for author %d: %w", authorID, err)
	}
	return nil
}

// LastEnrichmentAt reads when authorID was last enriched, nil when it never
// was or does not exist.
func (r *AuthorRepo) LastEnrichmentAt(ctx context.Context, authorID int64) (*time.Time, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT last_enrichment_at FROM authors WHERE id = ?`, authorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read last_enrichment_at for author %d: %w", authorID, err)
	}
	return parseFlexibleTime(raw)
}
