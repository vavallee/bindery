package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// ABSSeriesMatchRepo stores the per item outcome of the ABS import's Hardcover
// series match so an unchanged item is not searched again (#2173).
type ABSSeriesMatchRepo struct {
	db *sql.DB
}

func NewABSSeriesMatchRepo(db *sql.DB) *ABSSeriesMatchRepo {
	return &ABSSeriesMatchRepo{db: db}
}

// Get returns the recorded state for an item, or nil when there is none.
func (r *ABSSeriesMatchRepo) Get(ctx context.Context, sourceID, libraryID, itemID string) (*models.ABSSeriesMatchState, error) {
	var s models.ABSSeriesMatchState
	err := r.db.QueryRowContext(ctx, `
		SELECT source_id, library_id, item_id, input_hash, outcome, checked_at
		FROM abs_series_match_state
		WHERE source_id = ? AND library_id = ? AND item_id = ?`,
		sourceID, libraryID, itemID).Scan(&s.SourceID, &s.LibraryID, &s.ItemID, &s.InputHash, &s.Outcome, &s.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get abs series match state: %w", err)
	}
	return &s, nil
}

// Upsert records a match outcome, stamping checked_at with the given time.
func (r *ABSSeriesMatchRepo) Upsert(ctx context.Context, s *models.ABSSeriesMatchState, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO abs_series_match_state (source_id, library_id, item_id, input_hash, outcome, checked_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, library_id, item_id) DO UPDATE SET
			input_hash = excluded.input_hash,
			outcome = excluded.outcome,
			checked_at = excluded.checked_at`,
		s.SourceID, s.LibraryID, s.ItemID, s.InputHash, s.Outcome, at.UTC())
	if err != nil {
		return fmt.Errorf("upsert abs series match state: %w", err)
	}
	return nil
}
