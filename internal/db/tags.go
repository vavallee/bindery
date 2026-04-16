package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

type TagRepo struct {
	db *sql.DB
}

func NewTagRepo(db *sql.DB) *TagRepo {
	return &TagRepo{db: db}
}

func (r *TagRepo) List(ctx context.Context) ([]models.Tag, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, name FROM tags ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []models.Tag
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (r *TagRepo) Create(ctx context.Context, t *models.Tag) error {
	result, err := r.db.ExecContext(ctx, "INSERT INTO tags (name) VALUES (?)", t.Name)
	if err != nil {
		return fmt.Errorf("create tag: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get tag id: %w", err)
	}
	t.ID = id
	return nil
}

func (r *TagRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM tags WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete tag %d: %w", id, err)
	}
	return nil
}

func (r *TagRepo) GetAuthorTags(ctx context.Context, authorID int64) ([]models.Tag, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.name FROM tags t
		JOIN author_tags at ON at.tag_id = t.id
		WHERE at.author_id = ?
		ORDER BY t.name`, authorID)
	if err != nil {
		return nil, fmt.Errorf("get author tags: %w", err)
	}
	defer rows.Close()

	var tags []models.Tag
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (r *TagRepo) SetAuthorTags(ctx context.Context, authorID int64, tagIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM author_tags WHERE author_id=?", authorID); err != nil {
		return fmt.Errorf("clear author tags: %w", err)
	}

	for _, tagID := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO author_tags (author_id, tag_id) VALUES (?, ?)", authorID, tagID); err != nil {
			return fmt.Errorf("insert author tag: %w", err)
		}
	}

	return tx.Commit()
}

// GetByID fetches a single tag by ID. Returns nil if not found.
func (r *TagRepo) GetByID(ctx context.Context, id int64) (*models.Tag, error) {
	var t models.Tag
	err := r.db.QueryRowContext(ctx, "SELECT id, name FROM tags WHERE id=?", id).Scan(&t.ID, &t.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tag %d: %w", id, err)
	}
	return &t, nil
}
