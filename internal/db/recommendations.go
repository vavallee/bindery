package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// RecommendationRepo manages the recommendations, dismissals, and author
// exclusions tables.
type RecommendationRepo struct {
	db *sql.DB
}

// NewRecommendationRepo creates a new RecommendationRepo.
func NewRecommendationRepo(db *sql.DB) *RecommendationRepo {
	return &RecommendationRepo{db: db}
}

// ReplaceBatch atomically replaces all recommendations for a user. Runs a
// DELETE + INSERT inside a single transaction so readers never see a partial set.
func (r *RecommendationRepo) ReplaceBatch(ctx context.Context, userID int64, candidates []models.RecommendationCandidate) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM recommendations WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete old recs: %w", err)
	}

	now := time.Now().UTC()
	batchID := fmt.Sprintf("%d-%d", userID, now.Unix())

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO recommendations (
			user_id, foreign_id, rec_type, title, author_name, author_id,
			image_url, description, genres, rating, ratings_count,
			release_date, language, media_type, score, reason,
			series_id, series_pos, batch_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, c := range candidates {
		genresJSON, err := json.Marshal(c.Genres)
		if err != nil {
			slog.Warn("recommendations: marshal genres", "error", err)
		}
		if genresJSON == nil {
			genresJSON = []byte("[]")
		}

		_, err = stmt.ExecContext(ctx,
			userID, c.ForeignID, c.RecType, c.Title, c.AuthorName, c.AuthorID,
			c.ImageURL, c.Description, string(genresJSON), c.Rating, c.RatingsCount,
			c.ReleaseDate, c.Language, c.MediaType, c.Score, c.Reason,
			c.SeriesID, c.SeriesPos, batchID, now,
		)
		if err != nil {
			return fmt.Errorf("insert rec %q: %w", c.Title, err)
		}
	}

	return tx.Commit()
}

// List returns non-dismissed recommendations for a user, ordered by score DESC.
// An optional recType filter can be applied.
func (r *RecommendationRepo) List(ctx context.Context, userID int64, recType string, limit, offset int) ([]models.Recommendation, error) {
	q := "SELECT id, user_id, foreign_id, rec_type, title, author_name, author_id, image_url, description, genres, rating, ratings_count, release_date, language, media_type, score, reason, series_id, series_pos, dismissed, batch_id, created_at FROM recommendations WHERE user_id = ? AND dismissed = 0"
	args := []any{userID}

	if recType != "" {
		q += " AND rec_type = ?"
		args = append(args, recType)
	}

	q += " ORDER BY score DESC"

	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	if offset > 0 {
		q += " OFFSET ?"
		args = append(args, offset)
	}

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list recommendations: %w", err)
	}
	defer rows.Close()

	var recs []models.Recommendation
	for rows.Next() {
		var rec models.Recommendation
		var dismissed int
		var genresJSON string
		err := rows.Scan(
			&rec.ID, &rec.UserID, &rec.ForeignID, &rec.RecType,
			&rec.Title, &rec.AuthorName, &rec.AuthorID,
			&rec.ImageURL, &rec.Description, &genresJSON,
			&rec.Rating, &rec.RatingsCount, &rec.ReleaseDate,
			&rec.Language, &rec.MediaType, &rec.Score, &rec.Reason,
			&rec.SeriesID, &rec.SeriesPos, &dismissed, &rec.BatchID,
			&rec.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan recommendation: %w", err)
		}
		_ = json.Unmarshal([]byte(genresJSON), &rec.Genres)
		rec.Dismissed = dismissed != 0
		recs = append(recs, rec)
	}
	return recs, rows.Err()
}

// GetByID returns a single recommendation by its ID.
func (r *RecommendationRepo) GetByID(ctx context.Context, id int64) (*models.Recommendation, error) {
	var rec models.Recommendation
	var dismissed int
	var genresJSON string
	err := r.db.QueryRowContext(ctx,
		"SELECT id, user_id, foreign_id, rec_type, title, author_name, author_id, image_url, description, genres, rating, ratings_count, release_date, language, media_type, score, reason, series_id, series_pos, dismissed, batch_id, created_at FROM recommendations WHERE id = ?",
		id,
	).Scan(
		&rec.ID, &rec.UserID, &rec.ForeignID, &rec.RecType,
		&rec.Title, &rec.AuthorName, &rec.AuthorID,
		&rec.ImageURL, &rec.Description, &genresJSON,
		&rec.Rating, &rec.RatingsCount, &rec.ReleaseDate,
		&rec.Language, &rec.MediaType, &rec.Score, &rec.Reason,
		&rec.SeriesID, &rec.SeriesPos, &dismissed, &rec.BatchID,
		&rec.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get recommendation %d: %w", id, err)
	}
	_ = json.Unmarshal([]byte(genresJSON), &rec.Genres)
	rec.Dismissed = dismissed != 0
	return &rec, nil
}

// Dismiss marks a recommendation as dismissed and records the dismissal in
// the persistent dismissals table so it survives regeneration.
func (r *RecommendationRepo) Dismiss(ctx context.Context, userID, recID int64) error {
	// Get the recommendation to find its foreign_id.
	rec, err := r.GetByID(ctx, recID)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("recommendation %d not found", recID)
	}

	// Mark as dismissed.
	if _, err := r.db.ExecContext(ctx,
		"UPDATE recommendations SET dismissed = 1 WHERE id = ?", recID); err != nil {
		return fmt.Errorf("dismiss rec %d: %w", recID, err)
	}

	// Persist in dismissals table.
	if _, err := r.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO recommendation_dismissals (user_id, foreign_id) VALUES (?, ?)",
		userID, rec.ForeignID); err != nil {
		return fmt.Errorf("record dismissal: %w", err)
	}

	return nil
}

// IsDismissed checks whether a foreign_id has been dismissed by the user.
func (r *RecommendationRepo) IsDismissed(ctx context.Context, userID int64, foreignID string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM recommendation_dismissals WHERE user_id = ? AND foreign_id = ?",
		userID, foreignID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListDismissedIDs returns all dismissed foreign_ids for a user.
func (r *RecommendationRepo) ListDismissedIDs(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT foreign_id FROM recommendation_dismissals WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

// ClearDismissals removes all dismissals for a user.
func (r *RecommendationRepo) ClearDismissals(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM recommendation_dismissals WHERE user_id = ?", userID)
	return err
}

// AddAuthorExclusion adds an author to the user's exclusion list.
func (r *RecommendationRepo) AddAuthorExclusion(ctx context.Context, userID int64, authorName string) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO recommendation_author_exclusions (user_id, author_name) VALUES (?, ?)",
		userID, authorName)
	return err
}

// RemoveAuthorExclusion removes an author from the user's exclusion list.
func (r *RecommendationRepo) RemoveAuthorExclusion(ctx context.Context, userID int64, authorName string) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM recommendation_author_exclusions WHERE user_id = ? AND author_name = ?",
		userID, authorName)
	return err
}

// ListAuthorExclusions returns all excluded author names for a user.
func (r *RecommendationRepo) ListAuthorExclusions(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT author_name FROM recommendation_author_exclusions WHERE user_id = ? ORDER BY author_name",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
