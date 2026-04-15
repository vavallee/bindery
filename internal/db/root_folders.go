package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type RootFolderRepo struct {
	db *sql.DB
}

func NewRootFolderRepo(db *sql.DB) *RootFolderRepo {
	return &RootFolderRepo{db: db}
}

func (r *RootFolderRepo) List(ctx context.Context) ([]models.RootFolder, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, path, free_space, created_at FROM root_folders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []models.RootFolder
	for rows.Next() {
		var f models.RootFolder
		if err := rows.Scan(&f.ID, &f.Path, &f.FreeSpace, &f.CreatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func (r *RootFolderRepo) GetByID(ctx context.Context, id int64) (*models.RootFolder, error) {
	var f models.RootFolder
	err := r.db.QueryRowContext(ctx,
		`SELECT id, path, free_space, created_at FROM root_folders WHERE id=?`, id).
		Scan(&f.ID, &f.Path, &f.FreeSpace, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &f, err
}

func (r *RootFolderRepo) Create(ctx context.Context, path string) (*models.RootFolder, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO root_folders (path, free_space, created_at) VALUES (?, 0, ?)`,
		path, now)
	if err != nil {
		return nil, fmt.Errorf("create root folder: %w", err)
	}
	id, _ := result.LastInsertId()
	return &models.RootFolder{ID: id, Path: path, CreatedAt: now}, nil
}

func (r *RootFolderRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM root_folders WHERE id=?`, id)
	return err
}

func (r *RootFolderRepo) UpdateFreeSpace(ctx context.Context, id int64, freeSpace int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE root_folders SET free_space=? WHERE id=?`, freeSpace, id)
	return err
}
