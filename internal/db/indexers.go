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
		SELECT id, name, type, url, api_key, categories, include_parent_categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, seed_ratio, seed_ratio_source, freeleech_only,
		       last_error, last_error_code, last_failure_at, last_success_at, created_at, updated_at
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
		SELECT id, name, type, url, api_key, categories, include_parent_categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, seed_ratio, seed_ratio_source, freeleech_only,
		       last_error, last_error_code, last_failure_at, last_success_at, created_at, updated_at
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
		SELECT id, name, type, url, api_key, categories, include_parent_categories, priority, enabled, supports_search,
		       prowlarr_instance_id, prowlarr_indexer_id, seed_ratio, seed_ratio_source, freeleech_only,
		       last_error, last_error_code, last_failure_at, last_success_at, created_at, updated_at
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
		INSERT INTO indexers (name, type, url, api_key, categories, include_parent_categories, priority, enabled, supports_search,
		                      prowlarr_instance_id, prowlarr_indexer_id, seed_ratio, seed_ratio_source, freeleech_only, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.Name, idx.Type, idx.URL, idx.APIKey, string(catsJSON), idx.IncludeParentCategories,
		idx.Priority, idx.Enabled, idx.SupportsSearch,
		idx.ProwlarrInstanceID, idx.ProwlarrIndexerID, idx.SeedRatio, idx.SeedRatioSource, idx.FreeleechOnly, now, now)
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
		UPDATE indexers SET name=?, type=?, url=?, api_key=?, categories=?, include_parent_categories=?, priority=?,
		                    enabled=?, supports_search=?, seed_ratio=?, seed_ratio_source=?, freeleech_only=?, updated_at=?
		WHERE id=?`,
		idx.Name, idx.Type, idx.URL, idx.APIKey, string(catsJSON), idx.IncludeParentCategories,
		idx.Priority, idx.Enabled, idx.SupportsSearch, idx.SeedRatio, idx.SeedRatioSource, idx.FreeleechOnly, now, idx.ID)
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

// UpdateAPIKeyByProwlarrInstance rewrites the api_key column for every indexer
// synced from the given Prowlarr instance. Called when the parent instance's
// API key is rotated via the settings UI, so callers do not have to wait for
// the next Prowlarr sync to refresh credentials on stored per-indexer rows.
func (r *IndexerRepo) UpdateAPIKeyByProwlarrInstance(ctx context.Context, instanceID int64, apiKey string) (int64, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx,
		"UPDATE indexers SET api_key=?, updated_at=? WHERE prowlarr_instance_id=?",
		apiKey, now, instanceID)
	if err != nil {
		return 0, fmt.Errorf("propagate prowlarr api key to indexers: %w", err)
	}
	return result.RowsAffected()
}

type indexerScanner interface {
	Scan(dest ...any) error
}

func scanIndexer(s indexerScanner) (models.Indexer, error) {
	var idx models.Indexer
	var includeParentCategories, enabled, supportsSearch, freeleechOnly int
	var catsJSON string
	if err := s.Scan(
		&idx.ID, &idx.Name, &idx.Type, &idx.URL, &idx.APIKey,
		&catsJSON, &includeParentCategories, &idx.Priority, &enabled, &supportsSearch,
		&idx.ProwlarrInstanceID, &idx.ProwlarrIndexerID, &idx.SeedRatio, &idx.SeedRatioSource, &freeleechOnly,
		&idx.LastError, &idx.LastErrorCode, &idx.LastFailureAt, &idx.LastSuccessAt,
		&idx.CreatedAt, &idx.UpdatedAt,
	); err != nil {
		return idx, err
	}
	idx.Enabled = enabled == 1
	idx.SupportsSearch = supportsSearch == 1
	idx.FreeleechOnly = freeleechOnly == 1
	idx.IncludeParentCategories = includeParentCategories == 1
	if err := json.Unmarshal([]byte(catsJSON), &idx.Categories); err != nil {
		return idx, fmt.Errorf("unmarshal indexer categories: %w", err)
	}
	return idx, nil
}

// RecordSearchFailure stores the outcome of a failed search against an indexer
// (#1935). code is the Newznab error code, or 0 when the failure was not a
// Newznab rejection (a connection error, a bad response body).
//
// updated_at is deliberately NOT touched. The rate-limit cooldown added by
// #1934 treats a bump of updated_at as the user having edited the row, and
// clears the cooldown on the assumption they fixed something. Health is written
// by the searcher on a schedule nobody chose, so bumping it here would keep
// resetting cooldowns and undo that fix.
func (r *IndexerRepo) RecordSearchFailure(ctx context.Context, id int64, code int, message string, at time.Time) error {
	var codePtr *int
	if code != 0 {
		codePtr = &code
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE indexers SET last_error=?, last_error_code=?, last_failure_at=?
		WHERE id=?`, message, codePtr, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("record indexer search failure: %w", err)
	}
	return nil
}

// RecordSearchSuccess clears any stored failure and stamps the last time this
// indexer answered. Clearing on success is what keeps a badge from outliving
// the problem it describes. See RecordSearchFailure on updated_at.
func (r *IndexerRepo) RecordSearchSuccess(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE indexers SET last_error=NULL, last_error_code=NULL, last_failure_at=NULL, last_success_at=?
		WHERE id=?`, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("record indexer search success: %w", err)
	}
	return nil
}
