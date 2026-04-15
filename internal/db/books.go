package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type BookRepo struct {
	db *sql.DB
}

func NewBookRepo(db *sql.DB) *BookRepo {
	return &BookRepo{db: db}
}

const bookColumns = `id, foreign_id, author_id, title, sort_title, original_title, description,
	image_url, release_date, genres, average_rating, ratings_count, monitored, status,
	any_edition_ok, selected_edition_id, file_path, language, media_type, narrator, duration_seconds, asin,
	calibre_id, metadata_provider, last_metadata_refresh_at, created_at, updated_at`

func (r *BookRepo) List(ctx context.Context) ([]models.Book, error) {
	return r.query(ctx, "SELECT "+bookColumns+" FROM books ORDER BY sort_title", nil)
}

func (r *BookRepo) ListByAuthor(ctx context.Context, authorID int64) ([]models.Book, error) {
	return r.query(ctx, "SELECT "+bookColumns+" FROM books WHERE author_id = ? ORDER BY release_date", []any{authorID})
}

func (r *BookRepo) ListByStatus(ctx context.Context, status string) ([]models.Book, error) {
	return r.query(ctx, "SELECT "+bookColumns+" FROM books WHERE status = ? AND monitored = 1 ORDER BY sort_title", []any{status})
}

func (r *BookRepo) GetByID(ctx context.Context, id int64) (*models.Book, error) {
	books, err := r.query(ctx, "SELECT "+bookColumns+" FROM books WHERE id = ?", []any{id})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (r *BookRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Book, error) {
	books, err := r.query(ctx, "SELECT "+bookColumns+" FROM books WHERE foreign_id = ?", []any{foreignID})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (r *BookRepo) Create(ctx context.Context, b *models.Book) error {
	now := time.Now().UTC()
	genresJSON, _ := json.Marshal(b.Genres)

	mediaType := b.MediaType
	if mediaType == "" {
		mediaType = models.MediaTypeEbook
	}

	result, err := r.db.ExecContext(ctx, `
		INSERT INTO books (foreign_id, author_id, title, sort_title, original_title, description,
		                   image_url, release_date, genres, average_rating, ratings_count,
		                   monitored, status, any_edition_ok, selected_edition_id,
		                   language, media_type, narrator, duration_seconds, asin,
		                   metadata_provider, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ForeignID, b.AuthorID, b.Title, b.SortTitle, b.OriginalTitle, b.Description,
		b.ImageURL, b.ReleaseDate, string(genresJSON), b.AverageRating, b.RatingsCount,
		b.Monitored, b.Status, b.AnyEditionOK, b.SelectedEditionID,
		b.Language, mediaType, b.Narrator, b.DurationSeconds, b.ASIN,
		b.MetadataProvider, now, now)
	if err != nil {
		return fmt.Errorf("create book: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get book id: %w", err)
	}
	b.ID = id
	b.CreatedAt = now
	b.UpdatedAt = now
	return nil
}

func (r *BookRepo) Update(ctx context.Context, b *models.Book) error {
	now := time.Now().UTC()
	genresJSON, _ := json.Marshal(b.Genres)

	mediaType := b.MediaType
	if mediaType == "" {
		mediaType = models.MediaTypeEbook
	}

	_, err := r.db.ExecContext(ctx, `
		UPDATE books SET title=?, sort_title=?, original_title=?, description=?, image_url=?,
		                 release_date=?, genres=?, average_rating=?, ratings_count=?,
		                 monitored=?, status=?, any_edition_ok=?, selected_edition_id=?,
		                 file_path=?, language=?, media_type=?, narrator=?, duration_seconds=?, asin=?,
		                 metadata_provider=?, last_metadata_refresh_at=?, updated_at=?
		WHERE id=?`,
		b.Title, b.SortTitle, b.OriginalTitle, b.Description, b.ImageURL,
		b.ReleaseDate, string(genresJSON), b.AverageRating, b.RatingsCount,
		b.Monitored, b.Status, b.AnyEditionOK, b.SelectedEditionID,
		b.FilePath, b.Language, mediaType, b.Narrator, b.DurationSeconds, b.ASIN,
		b.MetadataProvider, b.LastMetadataRefreshAt, now, b.ID)
	if err != nil {
		return fmt.Errorf("update book %d: %w", b.ID, err)
	}
	b.UpdatedAt = now
	return nil
}

func (r *BookRepo) SetFilePath(ctx context.Context, id int64, filePath string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE books SET file_path=?, status=? WHERE id=?",
		filePath, models.BookStatusImported, id)
	return err
}

// SetCalibreID stores the Calibre-assigned book id for the given Bindery
// book row. Called from the importer after a successful `calibredb add`.
func (r *BookRepo) SetCalibreID(ctx context.Context, id, calibreID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE books SET calibre_id=? WHERE id=?", calibreID, id)
	return err
}

// GetByCalibreID returns the Bindery book row that currently points at the
// given Calibre book id, or nil if none. The library import flow uses this
// as its primary idempotency key — a second import pass sees the existing
// row and updates in place instead of duplicating.
func (r *BookRepo) GetByCalibreID(ctx context.Context, calibreID int64) (*models.Book, error) {
	books, err := r.query(ctx, "SELECT "+bookColumns+" FROM books WHERE calibre_id = ?", []any{calibreID})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// FindByAuthorAndTitle locates a book under authorID whose title matches
// `title` case-insensitively. Used by the Calibre importer as a secondary
// dedupe path when the existing row has no calibre_id yet but the user
// (or a previous Bindery ingest) has already filed a book with the same
// title — re-matching by title links the two rows instead of creating a
// duplicate.
func (r *BookRepo) FindByAuthorAndTitle(ctx context.Context, authorID int64, title string) (*models.Book, error) {
	books, err := r.query(ctx,
		"SELECT "+bookColumns+" FROM books WHERE author_id = ? AND LOWER(title) = LOWER(?)",
		[]any{authorID, title})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

func (r *BookRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM books WHERE id=?", id)
	return err
}

func (r *BookRepo) query(ctx context.Context, q string, args []any) ([]models.Book, error) {
	var rows *sql.Rows
	var err error
	if args != nil {
		rows, err = r.db.QueryContext(ctx, q, args...)
	} else {
		rows, err = r.db.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer rows.Close()

	var books []models.Book
	for rows.Next() {
		var b models.Book
		var monitored, anyEditionOK int
		var genresStr string
		err := rows.Scan(
			&b.ID, &b.ForeignID, &b.AuthorID, &b.Title, &b.SortTitle,
			&b.OriginalTitle, &b.Description, &b.ImageURL, &b.ReleaseDate,
			&genresStr, &b.AverageRating, &b.RatingsCount,
			&monitored, &b.Status, &anyEditionOK, &b.SelectedEditionID,
			&b.FilePath, &b.Language, &b.MediaType,
			&b.Narrator, &b.DurationSeconds, &b.ASIN,
			&b.CalibreID, &b.MetadataProvider, &b.LastMetadataRefreshAt,
			&b.CreatedAt, &b.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		b.Monitored = monitored == 1
		b.AnyEditionOK = anyEditionOK == 1
		_ = json.Unmarshal([]byte(genresStr), &b.Genres)
		if b.Genres == nil {
			b.Genres = []string{}
		}
		books = append(books, b)
	}
	return books, rows.Err()
}
