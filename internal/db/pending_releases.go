package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// PendingReleaseRepo manages the pending_releases table.
type PendingReleaseRepo struct {
	db *sql.DB
}

// NewPendingReleaseRepo creates a new PendingReleaseRepo.
func NewPendingReleaseRepo(db *sql.DB) *PendingReleaseRepo {
	return &PendingReleaseRepo{db: db}
}

// Upsert inserts a pending release or ignores if the GUID already exists.
func (r *PendingReleaseRepo) Upsert(ctx context.Context, pr *models.PendingRelease) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pending_releases
		  (book_id, title, indexer_id, guid, protocol, size, age_minutes, quality,
		   custom_score, reason, first_seen, release_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(guid) DO UPDATE SET
		  reason      = excluded.reason,
		  age_minutes = excluded.age_minutes`,
		pr.BookID, pr.Title, pr.IndexerID, pr.GUID, pr.Protocol,
		pr.Size, pr.AgeMinutes, pr.Quality, pr.CustomScore,
		pr.Reason, time.Now().UTC(), pr.ReleaseJSON,
	)
	return err
}

// List returns all pending releases ordered by first_seen descending.
func (r *PendingReleaseRepo) List(ctx context.Context) ([]models.PendingRelease, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases
		ORDER BY first_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// ListByBook returns all pending releases for a specific book.
func (r *PendingReleaseRepo) ListByBook(ctx context.Context, bookID int64) ([]models.PendingRelease, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases WHERE book_id = ?
		ORDER BY first_seen DESC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// GetByID returns a single pending release by its ID.
func (r *PendingReleaseRepo) GetByID(ctx context.Context, id int64) (*models.PendingRelease, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, book_id, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases WHERE id = ?`, id)
	pr, err := scanPendingRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return pr, err
}

// DeleteByID removes a pending release.
func (r *PendingReleaseRepo) DeleteByID(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_releases WHERE id = ?`, id)
	return err
}

// DeleteByGUID removes a pending release by GUID.
func (r *PendingReleaseRepo) DeleteByGUID(ctx context.Context, guid string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_releases WHERE guid = ?`, guid)
	return err
}

// DeleteByBook removes all pending releases for a book.
func (r *PendingReleaseRepo) DeleteByBook(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_releases WHERE book_id = ?`, bookID)
	return err
}

func scanPendingReleases(rows *sql.Rows) ([]models.PendingRelease, error) {
	var out []models.PendingRelease
	for rows.Next() {
		pr, err := scanPendingReleaseRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pr)
	}
	return out, rows.Err()
}

func scanPendingRelease(row *sql.Row) (*models.PendingRelease, error) {
	return scanPendingReleaseRow(row)
}

type pendingScanner interface {
	Scan(dest ...any) error
}

func scanPendingReleaseRow(s pendingScanner) (*models.PendingRelease, error) {
	var pr models.PendingRelease
	var indexerID sql.NullInt64
	var quality sql.NullString
	err := s.Scan(
		&pr.ID, &pr.BookID, &pr.Title, &indexerID, &pr.GUID,
		&pr.Protocol, &pr.Size, &pr.AgeMinutes, &quality,
		&pr.CustomScore, &pr.Reason, &pr.FirstSeen, &pr.ReleaseJSON,
	)
	if err != nil {
		return nil, err
	}
	if indexerID.Valid {
		pr.IndexerID = &indexerID.Int64
	}
	pr.Quality = quality.String
	return &pr, nil
}
