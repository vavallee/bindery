package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type SeriesRepo struct {
	db *sql.DB
}

func NewSeriesRepo(db *sql.DB) *SeriesRepo {
	return &SeriesRepo{db: db}
}

func (r *SeriesRepo) List(ctx context.Context) ([]models.Series, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, foreign_id, title, description, monitored, created_at FROM series ORDER BY title")
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	defer rows.Close()

	var series []models.Series
	for rows.Next() {
		var s models.Series
		var monitored int
		if err := rows.Scan(&s.ID, &s.ForeignID, &s.Title, &s.Description, &monitored, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan series: %w", err)
		}
		s.Monitored = monitored == 1
		series = append(series, s)
	}
	return series, rows.Err()
}

func (r *SeriesRepo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	val := 0
	if monitored {
		val = 1
	}
	_, err := r.db.ExecContext(ctx, "UPDATE series SET monitored=? WHERE id=?", val, id)
	return err
}

// ListBooksInSeries returns all books linked to the given series, with status.
func (r *SeriesRepo) ListBooksInSeries(ctx context.Context, seriesID int64) ([]models.Book, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT b.id, b.foreign_id, b.author_id, b.title, b.sort_title, b.status, b.monitored,
		       b.image_url, b.release_date, b.language, b.media_type, b.created_at, b.updated_at
		FROM series_books sb
		JOIN books b ON b.id = sb.book_id
		WHERE sb.series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var books []models.Book
	for rows.Next() {
		var b models.Book
		var monitored int
		var releaseDate sql.NullTime
		if err := rows.Scan(
			&b.ID, &b.ForeignID, &b.AuthorID, &b.Title, &b.SortTitle, &b.Status, &monitored,
			&b.ImageURL, &releaseDate, &b.Language, &b.MediaType, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		b.Monitored = monitored == 1
		if releaseDate.Valid {
			b.ReleaseDate = &releaseDate.Time
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

func (r *SeriesRepo) GetByID(ctx context.Context, id int64) (*models.Series, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT id, foreign_id, title, description, monitored, created_at FROM series WHERE id=?", id)

	var s models.Series
	var monitored int
	err := row.Scan(&s.ID, &s.ForeignID, &s.Title, &s.Description, &monitored, &s.CreatedAt)
	s.Monitored = monitored == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get series %d: %w", id, err)
	}

	// Fetch series books with minimal book data
	bookRows, err := r.db.QueryContext(ctx, `
		SELECT sb.series_id, sb.book_id, sb.position_in_series, sb.primary_series,
		       b.id, b.foreign_id, b.author_id, b.title, b.sort_title, b.status,
		       b.monitored, b.image_url, b.created_at, b.updated_at
		FROM series_books sb
		JOIN books b ON b.id = sb.book_id
		WHERE sb.series_id = ?
		ORDER BY sb.position_in_series`, id)
	if err != nil {
		return &s, nil
	}
	defer bookRows.Close()

	for bookRows.Next() {
		var sb models.SeriesBook
		var b models.Book
		var monitored, primarySeries int
		err := bookRows.Scan(
			&sb.SeriesID, &sb.BookID, &sb.PositionInSeries, &primarySeries,
			&b.ID, &b.ForeignID, &b.AuthorID, &b.Title, &b.SortTitle, &b.Status,
			&monitored, &b.ImageURL, &b.CreatedAt, &b.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan series book: %w", err)
		}
		b.Monitored = monitored == 1
		sb.PrimarySeries = primarySeries == 1
		sb.Book = &b
		s.Books = append(s.Books, sb)
	}

	return &s, bookRows.Err()
}

func (r *SeriesRepo) Create(ctx context.Context, s *models.Series) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO series (foreign_id, title, description, created_at) VALUES (?, ?, ?, ?)",
		s.ForeignID, s.Title, s.Description, now)
	if err != nil {
		return fmt.Errorf("create series: %w", err)
	}
	id, _ := result.LastInsertId()
	s.ID = id
	s.CreatedAt = now
	return nil
}

// CreateOrGet inserts the series if its foreign_id does not yet exist, then
// populates s.ID with the persisted row's ID. Safe to call concurrently; the
// INSERT OR IGNORE prevents duplicate-key errors.
func (r *SeriesRepo) CreateOrGet(ctx context.Context, s *models.Series) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO series (foreign_id, title, description, created_at) VALUES (?, ?, ?, ?)",
		s.ForeignID, s.Title, s.Description, now)
	if err != nil {
		return fmt.Errorf("upsert series: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		id, _ := result.LastInsertId()
		s.ID = id
		s.CreatedAt = now
		return nil
	}
	// Row already existed — fetch its ID.
	row := r.db.QueryRowContext(ctx, "SELECT id FROM series WHERE foreign_id = ?", s.ForeignID)
	if err := row.Scan(&s.ID); err != nil {
		return fmt.Errorf("get existing series id: %w", err)
	}
	return nil
}

// LinkBook inserts a series_books row joining seriesID → bookID.
// INSERT OR IGNORE makes the call idempotent: a second call with the same
// pair is a no-op, which is safe for re-runs (e.g. reconcile-series).
func (r *SeriesRepo) LinkBook(ctx context.Context, seriesID, bookID int64, position string, primary bool) error {
	primaryInt := 0
	if primary {
		primaryInt = 1
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO series_books (series_id, book_id, position_in_series, primary_series)
		 VALUES (?, ?, ?, ?)`,
		seriesID, bookID, position, primaryInt)
	if err != nil {
		return fmt.Errorf("link book %d to series %d: %w", bookID, seriesID, err)
	}
	return nil
}

func (r *SeriesRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM series WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete series %d: %w", id, err)
	}
	return nil
}
