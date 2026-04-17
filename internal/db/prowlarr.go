package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type ProwlarrRepo struct {
	db *sql.DB
}

func NewProwlarrRepo(db *sql.DB) *ProwlarrRepo {
	return &ProwlarrRepo{db: db}
}

func (r *ProwlarrRepo) List(ctx context.Context) ([]models.ProwlarrInstance, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, url, api_key, sync_on_startup, enabled, last_sync_at, created_at, updated_at
		FROM prowlarr_instances ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []models.ProwlarrInstance
	for rows.Next() {
		p, err := scanProwlarr(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, p)
	}
	return instances, rows.Err()
}

func (r *ProwlarrRepo) GetByID(ctx context.Context, id int64) (*models.ProwlarrInstance, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, url, api_key, sync_on_startup, enabled, last_sync_at, created_at, updated_at
		FROM prowlarr_instances WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	p, err := scanProwlarr(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

func (r *ProwlarrRepo) Create(ctx context.Context, p *models.ProwlarrInstance) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO prowlarr_instances (name, url, api_key, sync_on_startup, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.URL, p.APIKey, p.SyncOnStartup, p.Enabled, now, now)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	p.ID = id
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

func (r *ProwlarrRepo) Update(ctx context.Context, p *models.ProwlarrInstance) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE prowlarr_instances
		SET name=?, url=?, api_key=?, sync_on_startup=?, enabled=?, updated_at=?
		WHERE id=?`,
		p.Name, p.URL, p.APIKey, p.SyncOnStartup, p.Enabled, now, p.ID)
	if err == nil {
		p.UpdatedAt = now
	}
	return err
}

func (r *ProwlarrRepo) SetLastSyncAt(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE prowlarr_instances SET last_sync_at=?, updated_at=? WHERE id=?",
		t.UTC(), t.UTC(), id)
	return err
}

func (r *ProwlarrRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM prowlarr_instances WHERE id=?", id)
	return err
}

type prowlarrScanner interface {
	Scan(dest ...any) error
}

func scanProwlarr(s prowlarrScanner) (models.ProwlarrInstance, error) {
	var p models.ProwlarrInstance
	var syncOnStartup, enabled int
	var lastSyncAt sql.NullTime
	err := s.Scan(
		&p.ID, &p.Name, &p.URL, &p.APIKey,
		&syncOnStartup, &enabled, &lastSyncAt,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.SyncOnStartup = syncOnStartup == 1
	p.Enabled = enabled == 1
	if lastSyncAt.Valid {
		p.LastSyncAt = &lastSyncAt.Time
	}
	return p, nil
}
