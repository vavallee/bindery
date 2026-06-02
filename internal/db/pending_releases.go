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
		  (book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes, quality,
		   custom_score, reason, first_seen, release_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(guid) DO UPDATE SET
		  reason      = excluded.reason,
		  age_minutes = excluded.age_minutes`,
		pr.BookID, pr.MediaType, pr.Title, pr.IndexerID, pr.GUID, pr.Protocol,
		pr.Size, pr.AgeMinutes, pr.Quality, pr.CustomScore,
		pr.Reason, time.Now().UTC(), pr.ReleaseJSON,
	)
	return err
}

// List returns all pending releases ordered by first_seen descending.
func (r *PendingReleaseRepo) List(ctx context.Context) ([]models.PendingRelease, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases
		ORDER BY first_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// ListByBook returns all pending releases for a specific book across all formats.
func (r *PendingReleaseRepo) ListByBook(ctx context.Context, bookID int64) ([]models.PendingRelease, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases WHERE book_id = ?
		ORDER BY first_seen DESC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// ListByBookAndMediaType returns pending releases for a specific book and format.
// Use this in preference to ListByBook when re-evaluating candidates for a
// single format so that the other format's entries are not disturbed.
func (r *PendingReleaseRepo) ListByBookAndMediaType(ctx context.Context, bookID int64, mediaType string) ([]models.PendingRelease, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases WHERE book_id = ? AND media_type = ?
		ORDER BY first_seen DESC`, bookID, mediaType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// ListForUser returns pending releases whose referenced book is owned by
// userID. pending_releases has no owner_user_id column of its own — ownership
// is derived from books.owner_user_id via the book_id FK. When userID == 0
// this falls back to List, preserving the unscoped admin/disabled-auth path.
func (r *PendingReleaseRepo) ListForUser(ctx context.Context, userID int64) ([]models.PendingRelease, error) {
	if userID == 0 {
		return r.List(ctx)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes,
		       quality, custom_score, reason, first_seen, release_json
		FROM pending_releases
		WHERE book_id IN (SELECT id FROM books WHERE owner_user_id = ?)
		ORDER BY first_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingReleases(rows)
}

// GetOwnerByID resolves the owning user for a pending release by joining
// through its referenced book. See HistoryRepo.GetOwnerByID for the return
// shape — they're identical.
func (r *PendingReleaseRepo) GetOwnerByID(ctx context.Context, id int64) (int64, bool, error) {
	var owner sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT b.owner_user_id
		  FROM pending_releases pr
		  LEFT JOIN books b ON b.id = pr.book_id
		 WHERE pr.id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return owner.Int64, true, nil
}

// GetByID returns a single pending release by its ID.
func (r *PendingReleaseRepo) GetByID(ctx context.Context, id int64) (*models.PendingRelease, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, book_id, media_type, title, indexer_id, guid, protocol, size, age_minutes,
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

// DeleteByBook removes all pending releases for a book across all formats.
func (r *PendingReleaseRepo) DeleteByBook(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_releases WHERE book_id = ?`, bookID)
	return err
}

// DeleteByBookAndMediaType removes pending releases for a book scoped to a
// single media type ("ebook" or "audiobook"). Prefer this over DeleteByBook
// when only one format has been grabbed so the other format's pending entries
// are preserved (see #707).
func (r *PendingReleaseRepo) DeleteByBookAndMediaType(ctx context.Context, bookID int64, mediaType string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_releases WHERE book_id = ? AND media_type = ?`, bookID, mediaType)
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
		&pr.ID, &pr.BookID, &pr.MediaType, &pr.Title, &indexerID, &pr.GUID,
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
