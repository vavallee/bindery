package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

type QualityProfileRepo struct {
	db *sql.DB
}

func NewQualityProfileRepo(db *sql.DB) *QualityProfileRepo {
	return &QualityProfileRepo{db: db}
}

func (r *QualityProfileRepo) List(ctx context.Context) ([]models.QualityProfile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at FROM quality_profiles ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list quality profiles: %w", err)
	}
	defer rows.Close()

	var profiles []models.QualityProfile
	for rows.Next() {
		p, err := scanQualityProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (r *QualityProfileRepo) GetByID(ctx context.Context, id int64) (*models.QualityProfile, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, upgrade_allowed, cutoff, items, created_at FROM quality_profiles WHERE id=?", id)
	if err != nil {
		return nil, fmt.Errorf("get quality profile %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	p, err := scanQualityProfile(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

func scanQualityProfile(rows *sql.Rows) (models.QualityProfile, error) {
	var p models.QualityProfile
	var upgradeAllowed int
	var itemsJSON string
	if err := rows.Scan(&p.ID, &p.Name, &upgradeAllowed, &p.Cutoff, &itemsJSON, &p.CreatedAt); err != nil {
		return p, fmt.Errorf("scan quality profile: %w", err)
	}
	p.UpgradeAllowed = upgradeAllowed == 1
	if err := json.Unmarshal([]byte(itemsJSON), &p.Items); err != nil {
		return p, fmt.Errorf("unmarshal quality profile items: %w", err)
	}
	if p.Items == nil {
		p.Items = []models.QualityItem{}
	}
	return p, nil
}
