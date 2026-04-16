package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type IndexerRepo struct {
	db *sql.DB
}

func NewIndexerRepo(db *sql.DB) *IndexerRepo {
	return &IndexerRepo{db: db}
}

func (r *IndexerRepo) List(ctx context.Context) ([]models.Indexer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, categories, priority, enabled, supports_search, created_at, updated_at
		FROM indexers ORDER BY priority`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexers []models.Indexer
	for rows.Next() {
		var idx models.Indexer
		var enabled, supportsSearch int
		var catsJSON string
		if err := rows.Scan(&idx.ID, &idx.Name, &idx.Type, &idx.URL, &idx.APIKey,
			&catsJSON, &idx.Priority, &enabled, &supportsSearch, &idx.CreatedAt, &idx.UpdatedAt); err != nil {
			return nil, err
		}
		idx.Enabled = enabled == 1
		idx.SupportsSearch = supportsSearch == 1
		if err := json.Unmarshal([]byte(catsJSON), &idx.Categories); err != nil {
			return nil, fmt.Errorf("unmarshal indexer categories: %w", err)
		}
		indexers = append(indexers, idx)
	}
	return indexers, rows.Err()
}

func (r *IndexerRepo) GetByID(ctx context.Context, id int64) (*models.Indexer, error) {
	var idx models.Indexer
	var enabled, supportsSearch int
	var catsJSON string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, type, url, api_key, categories, priority, enabled, supports_search, created_at, updated_at
		FROM indexers WHERE id=?`, id).
		Scan(&idx.ID, &idx.Name, &idx.Type, &idx.URL, &idx.APIKey,
			&catsJSON, &idx.Priority, &enabled, &supportsSearch, &idx.CreatedAt, &idx.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	idx.Enabled = enabled == 1
	idx.SupportsSearch = supportsSearch == 1
	if err = json.Unmarshal([]byte(catsJSON), &idx.Categories); err != nil {
		return nil, fmt.Errorf("unmarshal indexer categories: %w", err)
	}
	return &idx, nil
}

func (r *IndexerRepo) Create(ctx context.Context, idx *models.Indexer) error {
	now := time.Now().UTC()
	catsJSON, err := json.Marshal(idx.Categories)
	if err != nil {
		return fmt.Errorf("marshal indexer categories: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO indexers (name, type, url, api_key, categories, priority, enabled, supports_search, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.Name, idx.Type, idx.URL, idx.APIKey, string(catsJSON),
		idx.Priority, idx.Enabled, idx.SupportsSearch, now, now)
	if err != nil {
		return fmt.Errorf("create indexer: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get indexer id: %w", err)
	}
	idx.ID = id
	idx.CreatedAt = now
	idx.UpdatedAt = now
	return nil
}

func (r *IndexerRepo) Update(ctx context.Context, idx *models.Indexer) error {
	now := time.Now().UTC()
	catsJSON, err := json.Marshal(idx.Categories)
	if err != nil {
		return fmt.Errorf("marshal indexer categories: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		UPDATE indexers SET name=?, type=?, url=?, api_key=?, categories=?, priority=?,
		                    enabled=?, supports_search=?, updated_at=?
		WHERE id=?`,
		idx.Name, idx.Type, idx.URL, idx.APIKey, string(catsJSON),
		idx.Priority, idx.Enabled, idx.SupportsSearch, now, idx.ID)
	return err
}

func (r *IndexerRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM indexers WHERE id=?", id)
	return err
}
