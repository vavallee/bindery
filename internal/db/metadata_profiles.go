package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vavallee/bindery/internal/models"
)

type MetadataProfileRepo struct {
	db *sql.DB
}

func NewMetadataProfileRepo(db *sql.DB) *MetadataProfileRepo {
	return &MetadataProfileRepo{db: db}
}

func (r *MetadataProfileRepo) List(ctx context.Context) ([]models.MetadataProfile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, min_popularity, min_pages, skip_missing_date, skip_missing_isbn,
		       skip_part_books, allowed_languages, unknown_language_behavior, created_at
		FROM metadata_profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list metadata profiles: %w", err)
	}
	defer rows.Close()

	var profiles []models.MetadataProfile
	for rows.Next() {
		p, err := scanMetadataProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (r *MetadataProfileRepo) GetByID(ctx context.Context, id int64) (*models.MetadataProfile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, min_popularity, min_pages, skip_missing_date, skip_missing_isbn,
		       skip_part_books, allowed_languages, unknown_language_behavior, created_at
		FROM metadata_profiles WHERE id=?`, id)
	if err != nil {
		return nil, fmt.Errorf("get metadata profile %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	p, err := scanMetadataProfile(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

func (r *MetadataProfileRepo) Create(ctx context.Context, p *models.MetadataProfile) error {
	if p.UnknownLanguageBehavior == "" {
		p.UnknownLanguageBehavior = models.UnknownLanguagePass
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO metadata_profiles (name, min_popularity, min_pages, skip_missing_date,
		                               skip_missing_isbn, skip_part_books, allowed_languages,
		                               unknown_language_behavior)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.MinPopularity, p.MinPages,
		p.SkipMissingDate, p.SkipMissingISBN, p.SkipPartBooks, p.AllowedLanguages,
		p.UnknownLanguageBehavior)
	if err != nil {
		return fmt.Errorf("create metadata profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get metadata profile id: %w", err)
	}
	p.ID = id
	return nil
}

func (r *MetadataProfileRepo) Update(ctx context.Context, p *models.MetadataProfile) error {
	if p.UnknownLanguageBehavior == "" {
		p.UnknownLanguageBehavior = models.UnknownLanguagePass
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE metadata_profiles SET name=?, min_popularity=?, min_pages=?, skip_missing_date=?,
		                             skip_missing_isbn=?, skip_part_books=?, allowed_languages=?,
		                             unknown_language_behavior=?
		WHERE id=?`,
		p.Name, p.MinPopularity, p.MinPages,
		p.SkipMissingDate, p.SkipMissingISBN, p.SkipPartBooks, p.AllowedLanguages,
		p.UnknownLanguageBehavior, p.ID)
	if err != nil {
		return fmt.Errorf("update metadata profile %d: %w", p.ID, err)
	}
	return nil
}

func (r *MetadataProfileRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM metadata_profiles WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete metadata profile %d: %w", id, err)
	}
	return nil
}

func scanMetadataProfile(rows *sql.Rows) (models.MetadataProfile, error) {
	var p models.MetadataProfile
	var skipDate, skipISBN, skipPart int
	err := rows.Scan(
		&p.ID, &p.Name, &p.MinPopularity, &p.MinPages,
		&skipDate, &skipISBN, &skipPart, &p.AllowedLanguages,
		&p.UnknownLanguageBehavior, &p.CreatedAt,
	)
	if err != nil {
		return p, fmt.Errorf("scan metadata profile: %w", err)
	}
	p.SkipMissingDate = skipDate == 1
	p.SkipMissingISBN = skipISBN == 1
	p.SkipPartBooks = skipPart == 1
	return p, nil
}
