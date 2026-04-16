package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

type CustomFormatRepo struct {
	db *sql.DB
}

func NewCustomFormatRepo(db *sql.DB) *CustomFormatRepo {
	return &CustomFormatRepo{db: db}
}

func (r *CustomFormatRepo) List(ctx context.Context) ([]models.CustomFormat, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, conditions, created_at FROM custom_formats ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list custom formats: %w", err)
	}
	defer rows.Close()

	var formats []models.CustomFormat
	for rows.Next() {
		cf, err := scanCustomFormat(rows)
		if err != nil {
			return nil, err
		}
		formats = append(formats, cf)
	}
	return formats, rows.Err()
}

func (r *CustomFormatRepo) GetByID(ctx context.Context, id int64) (*models.CustomFormat, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, conditions, created_at FROM custom_formats WHERE id=?", id)
	if err != nil {
		return nil, fmt.Errorf("get custom format %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	cf, err := scanCustomFormat(rows)
	if err != nil {
		return nil, err
	}
	return &cf, rows.Err()
}

func (r *CustomFormatRepo) Create(ctx context.Context, cf *models.CustomFormat) error {
	condJSON, err := json.Marshal(cf.Conditions)
	if err != nil {
		return fmt.Errorf("marshal custom format conditions: %w", err)
	}
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO custom_formats (name, conditions) VALUES (?, ?)",
		cf.Name, string(condJSON))
	if err != nil {
		return fmt.Errorf("create custom format: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get custom format id: %w", err)
	}
	cf.ID = id
	return nil
}

func (r *CustomFormatRepo) Update(ctx context.Context, cf *models.CustomFormat) error {
	condJSON, err := json.Marshal(cf.Conditions)
	if err != nil {
		return fmt.Errorf("marshal custom format conditions: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		"UPDATE custom_formats SET name=?, conditions=? WHERE id=?",
		cf.Name, string(condJSON), cf.ID)
	if err != nil {
		return fmt.Errorf("update custom format %d: %w", cf.ID, err)
	}
	return nil
}

func (r *CustomFormatRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM custom_formats WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete custom format %d: %w", id, err)
	}
	return nil
}

func scanCustomFormat(rows *sql.Rows) (models.CustomFormat, error) {
	var cf models.CustomFormat
	var condJSON string
	err := rows.Scan(&cf.ID, &cf.Name, &condJSON, &cf.CreatedAt)
	if err != nil {
		return cf, fmt.Errorf("scan custom format: %w", err)
	}
	if err = json.Unmarshal([]byte(condJSON), &cf.Conditions); err != nil {
		return cf, fmt.Errorf("unmarshal custom format conditions: %w", err)
	}
	if cf.Conditions == nil {
		cf.Conditions = []models.CustomCondition{}
	}
	return cf, nil
}
