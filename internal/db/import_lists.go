package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type ImportListRepo struct {
	db *sql.DB
}

func NewImportListRepo(db *sql.DB) *ImportListRepo {
	return &ImportListRepo{db: db}
}

func (r *ImportListRepo) List(ctx context.Context) ([]models.ImportList, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, root_folder_id, quality_profile_id,
		       monitor_new, auto_add, enabled, media_type, last_sync_at, created_at, updated_at
		FROM import_lists ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list import lists: %w", err)
	}
	defer rows.Close()

	var lists []models.ImportList
	for rows.Next() {
		il, err := scanImportList(rows)
		if err != nil {
			return nil, err
		}
		lists = append(lists, il)
	}
	return lists, rows.Err()
}

func (r *ImportListRepo) ListByType(ctx context.Context, listType string) ([]models.ImportList, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, root_folder_id, quality_profile_id,
		       monitor_new, auto_add, enabled, media_type, last_sync_at, created_at, updated_at
		FROM import_lists WHERE type=? AND enabled=1 ORDER BY name`, listType)
	if err != nil {
		return nil, fmt.Errorf("list import lists by type: %w", err)
	}
	defer rows.Close()

	var lists []models.ImportList
	for rows.Next() {
		il, err := scanImportList(rows)
		if err != nil {
			return nil, err
		}
		lists = append(lists, il)
	}
	return lists, rows.Err()
}

func (r *ImportListRepo) UpdateLastSyncAt(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, "UPDATE import_lists SET last_sync_at=?, updated_at=? WHERE id=?", now, now, id)
	if err != nil {
		return fmt.Errorf("update last_sync_at for import list %d: %w", id, err)
	}
	return nil
}

func (r *ImportListRepo) GetByID(ctx context.Context, id int64) (*models.ImportList, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, url, api_key, root_folder_id, quality_profile_id,
		       monitor_new, auto_add, enabled, media_type, last_sync_at, created_at, updated_at
		FROM import_lists WHERE id=?`, id)
	if err != nil {
		return nil, fmt.Errorf("get import list %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	il, err := scanImportList(rows)
	if err != nil {
		return nil, err
	}
	return &il, rows.Err()
}

func (r *ImportListRepo) Create(ctx context.Context, il *models.ImportList) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO import_lists (name, type, url, api_key, root_folder_id, quality_profile_id,
		                          monitor_new, auto_add, enabled, media_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		il.Name, il.Type, il.URL, il.APIKey, il.RootFolderID, il.QualityProfileID,
		il.MonitorNew, il.AutoAdd, il.Enabled, il.MediaType, now, now)
	if err != nil {
		return fmt.Errorf("create import list: %w", err)
	}
	id, _ := result.LastInsertId()
	il.ID = id
	il.CreatedAt = now
	il.UpdatedAt = now
	return nil
}

func (r *ImportListRepo) Update(ctx context.Context, il *models.ImportList) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE import_lists SET name=?, type=?, url=?, api_key=?, root_folder_id=?,
		                        quality_profile_id=?, monitor_new=?, auto_add=?, enabled=?, media_type=?, updated_at=?
		WHERE id=?`,
		il.Name, il.Type, il.URL, il.APIKey, il.RootFolderID, il.QualityProfileID,
		il.MonitorNew, il.AutoAdd, il.Enabled, il.MediaType, now, il.ID)
	if err != nil {
		return fmt.Errorf("update import list %d: %w", il.ID, err)
	}
	il.UpdatedAt = now
	return nil
}

func (r *ImportListRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM import_lists WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete import list %d: %w", id, err)
	}
	return nil
}

func scanImportList(rows *sql.Rows) (models.ImportList, error) {
	var il models.ImportList
	var monitorNew, autoAdd, enabled int
	err := rows.Scan(
		&il.ID, &il.Name, &il.Type, &il.URL, &il.APIKey,
		&il.RootFolderID, &il.QualityProfileID,
		&monitorNew, &autoAdd, &enabled, &il.MediaType,
		&il.LastSyncAt, &il.CreatedAt, &il.UpdatedAt,
	)
	if err != nil {
		return il, fmt.Errorf("scan import list: %w", err)
	}
	il.MonitorNew = monitorNew == 1
	il.AutoAdd = autoAdd == 1
	il.Enabled = enabled == 1
	return il, nil
}

// --- Exclusions ---

func (r *ImportListRepo) ListExclusions(ctx context.Context) ([]models.ImportListExclusion, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, foreign_id, title, author_name, created_at FROM import_list_exclusions ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list import list exclusions: %w", err)
	}
	defer rows.Close()

	var exclusions []models.ImportListExclusion
	for rows.Next() {
		var e models.ImportListExclusion
		if err := rows.Scan(&e.ID, &e.ForeignID, &e.Title, &e.AuthorName, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan exclusion: %w", err)
		}
		exclusions = append(exclusions, e)
	}
	return exclusions, rows.Err()
}

func (r *ImportListRepo) CreateExclusion(ctx context.Context, e *models.ImportListExclusion) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO import_list_exclusions (foreign_id, title, author_name, created_at) VALUES (?, ?, ?, ?)",
		e.ForeignID, e.Title, e.AuthorName, now)
	if err != nil {
		return fmt.Errorf("create exclusion: %w", err)
	}
	id, _ := result.LastInsertId()
	e.ID = id
	e.CreatedAt = now
	return nil
}

func (r *ImportListRepo) DeleteExclusion(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM import_list_exclusions WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete exclusion %d: %w", id, err)
	}
	return nil
}
