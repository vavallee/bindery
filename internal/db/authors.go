package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type AuthorRepo struct {
	db *sql.DB
}

func NewAuthorRepo(db *sql.DB) *AuthorRepo {
	return &AuthorRepo{db: db}
}

const authorSelectCols = `id, foreign_id, name, sort_name, description, image_url, disambiguation,
	       ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
	       metadata_provider, last_metadata_refresh_at, created_at, updated_at`

func (r *AuthorRepo) List(ctx context.Context) ([]models.Author, error) {
	return r.ListByUser(ctx, 0)
}

const (
	listAuthorsAll    = "SELECT " + authorSelectCols + " FROM authors ORDER BY sort_name"
	listAuthorsByUser = "SELECT " + authorSelectCols + " FROM authors WHERE owner_user_id = ? ORDER BY sort_name"
)

func (r *AuthorRepo) ListByUser(ctx context.Context, userID int64) ([]models.Author, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userID == 0 {
		rows, err = r.db.QueryContext(ctx, listAuthorsAll)
	} else {
		rows, err = r.db.QueryContext(ctx, listAuthorsByUser, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	defer rows.Close()

	var authors []models.Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

func (r *AuthorRepo) GetByID(ctx context.Context, id int64) (*models.Author, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE id = ?`, id)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author %d: %w", id, err)
	}
	return &a, nil
}

func (r *AuthorRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Author, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+authorSelectCols+`
		FROM authors WHERE foreign_id = ?`, foreignID)

	a, err := scanAuthorRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get author by foreign_id %s: %w", foreignID, err)
	}
	return &a, nil
}

func (r *AuthorRepo) Create(ctx context.Context, a *models.Author) error {
	return r.CreateForUser(ctx, a, 0)
}

func (r *AuthorRepo) CreateForUser(ctx context.Context, a *models.Author, ownerUserID int64) error {
	now := time.Now().UTC()
	var ownerArg any
	if ownerUserID != 0 {
		ownerArg = ownerUserID
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO authors (foreign_id, name, sort_name, description, image_url, disambiguation,
		                     ratings_count, average_rating, monitored, quality_profile_id, metadata_profile_id, root_folder_id,
		                     metadata_provider, owner_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ForeignID, a.Name, a.SortName, a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID, a.MetadataProfileID, a.RootFolderID,
		a.MetadataProvider, ownerArg, now, now)
	if err != nil {
		return fmt.Errorf("create author: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get author id: %w", err)
	}
	a.ID = id
	a.CreatedAt = now
	a.UpdatedAt = now
	return nil
}

func (r *AuthorRepo) Update(ctx context.Context, a *models.Author) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE authors SET name=?, sort_name=?, description=?, image_url=?, disambiguation=?,
		                   ratings_count=?, average_rating=?, monitored=?, quality_profile_id=?,
		                   metadata_profile_id=?, root_folder_id=?, metadata_provider=?,
		                   last_metadata_refresh_at=?, updated_at=?
		WHERE id=?`,
		a.Name, a.SortName, a.Description, a.ImageURL, a.Disambiguation,
		a.RatingsCount, a.AverageRating, a.Monitored, a.QualityProfileID,
		a.MetadataProfileID, a.RootFolderID, a.MetadataProvider, a.LastMetadataRefreshAt, now, a.ID)
	if err != nil {
		return fmt.Errorf("update author %d: %w", a.ID, err)
	}
	a.UpdatedAt = now
	return nil
}

func (r *AuthorRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM authors WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete author %d: %w", id, err)
	}
	return nil
}

func scanAuthor(rows *sql.Rows) (models.Author, error) {
	var a models.Author
	var monitored int
	err := rows.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.MetadataProvider,
		&a.LastMetadataRefreshAt, &a.CreatedAt, &a.UpdatedAt)
	a.Monitored = monitored == 1
	return a, err
}

func scanAuthorRow(row *sql.Row) (models.Author, error) {
	var a models.Author
	var monitored int
	err := row.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Description, &a.ImageURL,
		&a.Disambiguation, &a.RatingsCount, &a.AverageRating, &monitored,
		&a.QualityProfileID, &a.MetadataProfileID, &a.RootFolderID, &a.MetadataProvider,
		&a.LastMetadataRefreshAt, &a.CreatedAt, &a.UpdatedAt)
	a.Monitored = monitored == 1
	return a, err
}
