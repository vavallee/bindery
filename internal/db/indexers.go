package db

import (
	"context"
	"database/sql"
	"encoding/json"
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
		SELECT id, name, type, url, api_key, categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, created_at, updated_at
		FROM indexers ORDER BY priority`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexers []models.Indexer
	for rows.Next() {
		idx, err := scanIndexer(rows)
		if err != nil {
			return nil, err
		}
		indexers = append(indexers, idx)
	}
	return indexers, rows.Err()
}

func (r *IndexerRepo) GetByID(ctx context.Context, id int64) (*models.Indexer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, created_at, updated_at
		FROM indexers WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	idx, err := scanIndexer(rows)
	if err != nil {
		return nil, err
	}
	return &idx, rows.Err()
}

// ListByProwlarrInstance returns all indexers managed by a specific Prowlarr instance.
func (r *IndexerRepo) ListByProwlarrInstance(ctx context.Context, instanceID int64) ([]models.Indexer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, created_at, updated_at
		FROM indexers WHERE prowlarr_instance_id=?`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexers []models.Indexer
	for rows.Next() {
		idx, err := scanIndexer(rows)
		if err != nil {
			return nil, err
		}
		indexers = append(indexers, idx)
	}
	return indexers, rows.Err()
}

func (r *IndexerRepo) Create(ctx context.Context, idx *models.Indexer) error {
	now := time.Now().UTC()
	catsJSON, err := json.Marshal(idx.Categories)
	if err != nil {
		return fmt.Errorf("marshal indexer categories: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO indexers (name, type, url, api_key, categories, priority, enabled, supports_search,
		                      prowlarr_instance_id, prowlarr_indexer_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.Name, idx.Type, idx.URL, idx.APIKey, string(catsJSON),
		idx.Priority, idx.Enabled, idx.SupportsSearch,
		idx.ProwlarrInstanceID, idx.ProwlarrIndexerID, now, now)
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

// DeleteByProwlarrInstance removes all indexers managed by a specific Prowlarr instance.
func (r *IndexerRepo) DeleteByProwlarrInstance(ctx context.Context, instanceID int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM indexers WHERE prowlarr_instance_id=?", instanceID)
	return err
}

type indexerScanner interface {
	Scan(dest ...any) error
}

func scanIndexer(s indexerScanner) (models.Indexer, error) {
	var idx models.Indexer
	var enabled, supportsSearch int
	var catsJSON string
	if err := s.Scan(
		&idx.ID, &idx.Name, &idx.Type, &idx.URL, &idx.APIKey,
		&catsJSON, &idx.Priority, &enabled, &supportsSearch,
		&idx.ProwlarrInstanceID, &idx.ProwlarrIndexerID,
		&idx.CreatedAt, &idx.UpdatedAt,
	); err != nil {
		return idx, err
	}
	idx.Enabled = enabled == 1
	idx.SupportsSearch = supportsSearch == 1
	if err := json.Unmarshal([]byte(catsJSON), &idx.Categories); err != nil {
		return idx, fmt.Errorf("unmarshal indexer categories: %w", err)
	}
	return idx, nil
}
