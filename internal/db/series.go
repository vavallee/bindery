package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.hydrateHardcoverLinks(ctx, series); err != nil {
		return nil, err
	}
	return series, nil
}

// ListWithBooks returns all series rows with their linked books populated.
func (r *SeriesRepo) ListWithBooks(ctx context.Context) ([]models.Series, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.foreign_id, s.title, s.description, s.monitored, s.created_at,
		       sb.series_id, sb.book_id, sb.position_in_series, sb.primary_series,
		       b.id, b.foreign_id, b.author_id, b.title, b.sort_title, b.status,
		       b.monitored, b.image_url, b.release_date, b.created_at, b.updated_at
		FROM series s
		LEFT JOIN series_books sb ON sb.series_id = s.id
		LEFT JOIN books b ON b.id = sb.book_id
		ORDER BY s.title, CAST(NULLIF(sb.position_in_series, '') AS REAL), sb.position_in_series, b.sort_title`)
	if err != nil {
		return nil, fmt.Errorf("list series with books: %w", err)
	}
	defer rows.Close()

	series := make([]models.Series, 0)
	byID := make(map[int64]int)
	for rows.Next() {
		var s models.Series
		var monitored int
		var sbSeriesID, sbBookID, bookID, authorID sql.NullInt64
		var position sql.NullString
		var primarySeries, bookMonitored sql.NullInt64
		var foreignID, title, sortTitle, status, imageURL sql.NullString
		var releaseDate, bookCreatedAt, bookUpdatedAt sql.NullTime
		if err := rows.Scan(
			&s.ID, &s.ForeignID, &s.Title, &s.Description, &monitored, &s.CreatedAt,
			&sbSeriesID, &sbBookID, &position, &primarySeries,
			&bookID, &foreignID, &authorID, &title, &sortTitle, &status,
			&bookMonitored, &imageURL, &releaseDate, &bookCreatedAt, &bookUpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan series with books: %w", err)
		}
		s.Monitored = monitored == 1

		idx, ok := byID[s.ID]
		if !ok {
			idx = len(series)
			series = append(series, s)
			byID[s.ID] = idx
		}
		if !sbBookID.Valid || !bookID.Valid {
			continue
		}

		book := models.Book{
			ID:        bookID.Int64,
			ForeignID: foreignID.String,
			AuthorID:  authorID.Int64,
			Title:     title.String,
			SortTitle: sortTitle.String,
			Status:    status.String,
			Monitored: bookMonitored.Int64 == 1,
			ImageURL:  imageURL.String,
		}
		if releaseDate.Valid {
			book.ReleaseDate = &releaseDate.Time
		}
		if bookCreatedAt.Valid {
			book.CreatedAt = bookCreatedAt.Time
		}
		if bookUpdatedAt.Valid {
			book.UpdatedAt = bookUpdatedAt.Time
		}
		series[idx].Books = append(series[idx].Books, models.SeriesBook{
			SeriesID:         sbSeriesID.Int64,
			BookID:           sbBookID.Int64,
			PositionInSeries: position.String,
			PrimarySeries:    primarySeries.Int64 == 1,
			Book:             &book,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.hydrateHardcoverLinks(ctx, series); err != nil {
		return nil, err
	}
	return series, nil
}

func (r *SeriesRepo) hydrateHardcoverLinks(ctx context.Context, series []models.Series) error {
	if len(series) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(series))
	args := make([]any, 0, len(series))
	for _, s := range series {
		placeholders = append(placeholders, "?")
		args = append(args, s.ID)
	}
	query := `
		SELECT id, series_id, hardcover_series_id, hardcover_provider_id, hardcover_title,
		       hardcover_author_name, hardcover_book_count, link_confidence, linked_by,
		       linked_at, created_at, updated_at
		FROM series_hardcover_links
		WHERE series_id IN (` + strings.Join(placeholders, ",") + `)` // #nosec G202 -- placeholders are generated from fixed ? tokens; series IDs remain bound args
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("list series hardcover links: %w", err)
	}
	defer rows.Close()

	links := make(map[int64]models.SeriesHardcoverLink)
	for rows.Next() {
		link, err := scanSeriesHardcoverLink(rows)
		if err != nil {
			return err
		}
		links[link.SeriesID] = link
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range series {
		if link, ok := links[series[i].ID]; ok {
			linkCopy := link
			series[i].HardcoverLink = &linkCopy
		}
	}
	return nil
}

func (r *SeriesRepo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	val := 0
	if monitored {
		val = 1
	}
	_, err := r.db.ExecContext(ctx, "UPDATE series SET monitored=? WHERE id=?", val, id)
	return err
}

func (r *SeriesRepo) CreateManual(ctx context.Context, title string) (*models.Series, error) {
	s := &models.Series{
		ForeignID:   fmt.Sprintf("manual:series:%d", time.Now().UTC().UnixNano()),
		Title:       strings.TrimSpace(title),
		Description: "",
	}
	if err := r.Create(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (r *SeriesRepo) UpdateTitle(ctx context.Context, id int64, title string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE series SET title=? WHERE id=?", strings.TrimSpace(title), id)
	if err != nil {
		return fmt.Errorf("update series %d title: %w", id, err)
	}
	return nil
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

	if err := bookRows.Err(); err != nil {
		return nil, err
	}
	link, err := r.GetHardcoverLink(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	s.HardcoverLink = link
	return &s, nil
}

type seriesHardcoverLinkScanner interface {
	Scan(dest ...any) error
}

func scanSeriesHardcoverLink(scanner seriesHardcoverLinkScanner) (models.SeriesHardcoverLink, error) {
	var link models.SeriesHardcoverLink
	err := scanner.Scan(
		&link.ID,
		&link.SeriesID,
		&link.HardcoverSeriesID,
		&link.HardcoverProviderID,
		&link.HardcoverTitle,
		&link.HardcoverAuthorName,
		&link.HardcoverBookCount,
		&link.Confidence,
		&link.LinkedBy,
		&link.LinkedAt,
		&link.CreatedAt,
		&link.UpdatedAt,
	)
	return link, err
}

func (r *SeriesRepo) GetHardcoverLink(ctx context.Context, seriesID int64) (*models.SeriesHardcoverLink, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, series_id, hardcover_series_id, hardcover_provider_id, hardcover_title,
		       hardcover_author_name, hardcover_book_count, link_confidence, linked_by,
		       linked_at, created_at, updated_at
		FROM series_hardcover_links
		WHERE series_id = ?`, seriesID)
	link, err := scanSeriesHardcoverLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get series hardcover link %d: %w", seriesID, err)
	}
	return &link, nil
}

func (r *SeriesRepo) UpsertHardcoverLink(ctx context.Context, link *models.SeriesHardcoverLink) error {
	if link == nil {
		return nil
	}
	if link.SeriesID == 0 {
		return errors.New("series id is required")
	}
	if strings.TrimSpace(link.HardcoverSeriesID) == "" {
		return errors.New("hardcover series id is required")
	}
	now := time.Now().UTC()
	if link.LinkedAt.IsZero() {
		link.LinkedAt = now
	}
	if link.LinkedBy == "" {
		link.LinkedBy = "manual"
	}
	if link.HardcoverProviderID == "" {
		link.HardcoverProviderID = strings.TrimPrefix(link.HardcoverSeriesID, "hc-series:")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO series_hardcover_links (
			series_id, hardcover_series_id, hardcover_provider_id, hardcover_title,
			hardcover_author_name, hardcover_book_count, link_confidence, linked_by,
			linked_at, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(series_id) DO UPDATE SET
			hardcover_series_id = excluded.hardcover_series_id,
			hardcover_provider_id = excluded.hardcover_provider_id,
			hardcover_title = excluded.hardcover_title,
			hardcover_author_name = excluded.hardcover_author_name,
			hardcover_book_count = excluded.hardcover_book_count,
			link_confidence = excluded.link_confidence,
			linked_by = excluded.linked_by,
			linked_at = excluded.linked_at,
			updated_at = excluded.updated_at`,
		link.SeriesID,
		link.HardcoverSeriesID,
		link.HardcoverProviderID,
		link.HardcoverTitle,
		link.HardcoverAuthorName,
		link.HardcoverBookCount,
		link.Confidence,
		link.LinkedBy,
		link.LinkedAt,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert series hardcover link: %w", err)
	}
	stored, err := r.GetHardcoverLink(ctx, link.SeriesID)
	if err != nil {
		return err
	}
	if stored != nil {
		*link = *stored
	}
	return nil
}

func (r *SeriesRepo) DeleteHardcoverLink(ctx context.Context, seriesID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM series_hardcover_links WHERE series_id = ?`, seriesID)
	if err != nil {
		return fmt.Errorf("delete series hardcover link %d: %w", seriesID, err)
	}
	return nil
}

func (r *SeriesRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Series, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT id, foreign_id, title, description, monitored, created_at FROM series WHERE foreign_id=?", foreignID)
	var s models.Series
	var monitored int
	err := row.Scan(&s.ID, &s.ForeignID, &s.Title, &s.Description, &monitored, &s.CreatedAt)
	s.Monitored = monitored == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get series by foreign_id %s: %w", foreignID, err)
	}
	return &s, nil
}

func (r *SeriesRepo) UpdateForeignID(ctx context.Context, id int64, foreignID string) error {
	if id == 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, "UPDATE series SET foreign_id=? WHERE id=?", foreignID, id)
	if err != nil {
		return fmt.Errorf("update series %d foreign_id: %w", id, err)
	}
	return nil
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
	_, err := r.LinkBookIfMissing(ctx, seriesID, bookID, position, primary)
	return err
}

// LinkBookIfMissing inserts a series_books row and reports whether it created
// the membership. Callers that record rollback ownership should only claim
// ownership when this returns true.
func (r *SeriesRepo) LinkBookIfMissing(ctx context.Context, seriesID, bookID int64, position string, primary bool) (bool, error) {
	primaryInt := 0
	if primary {
		primaryInt = 1
	}
	result, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO series_books (series_id, book_id, position_in_series, primary_series)
		 VALUES (?, ?, ?, ?)`,
		seriesID, bookID, position, primaryInt)
	if err != nil {
		return false, fmt.Errorf("link book %d to series %d: %w", bookID, seriesID, err)
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func (r *SeriesRepo) UpsertBookLink(ctx context.Context, seriesID, bookID int64, position string, primary bool) error {
	primaryInt := 0
	if primary {
		primaryInt = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO series_books (series_id, book_id, position_in_series, primary_series)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(series_id, book_id) DO UPDATE SET
			position_in_series = excluded.position_in_series,
			primary_series = excluded.primary_series`,
		seriesID, bookID, strings.TrimSpace(position), primaryInt)
	if err != nil {
		return fmt.Errorf("upsert book %d in series %d: %w", bookID, seriesID, err)
	}
	return nil
}

func (r *SeriesRepo) UnlinkBook(ctx context.Context, seriesID, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM series_books WHERE series_id = ? AND book_id = ?`, seriesID, bookID)
	if err != nil {
		return fmt.Errorf("unlink book %d from series %d: %w", bookID, seriesID, err)
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

// GetPrimarySeriesForBook returns the title and position of the primary series
// for the given book. Returns ("", "", nil) when the book has no primary series.
func (r *SeriesRepo) GetPrimarySeriesForBook(ctx context.Context, bookID int64) (seriesTitle, position string, err error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT s.title, sb.position_in_series
		FROM series_books sb
		JOIN series s ON s.id = sb.series_id
		WHERE sb.book_id = ? AND sb.primary_series = 1
		LIMIT 1`, bookID)
	err = row.Scan(&seriesTitle, &position)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return seriesTitle, position, err
}

// GetBookBySeriesPosition finds the single "wanted" book at the given position
// within any series whose title matches seriesTitle (case-insensitive, trimmed).
// Returns nil when no match is found or when the result is ambiguous (more than
// one book matches), to avoid false-positive reconciliation.
func (r *SeriesRepo) GetBookBySeriesPosition(ctx context.Context, seriesTitle, position string) (*models.Book, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id FROM series WHERE lower(trim(title)) = lower(trim(?))", seriesTitle)
	if err != nil {
		return nil, fmt.Errorf("series title lookup: %w", err)
	}
	defer rows.Close()

	var seriesIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		seriesIDs = append(seriesIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(seriesIDs) == 0 {
		return nil, nil
	}

	var found []*models.Book
	for _, sid := range seriesIDs {
		row := r.db.QueryRowContext(ctx, `
			SELECT b.id, b.foreign_id, b.author_id, b.title, b.sort_title, b.status,
			       b.monitored, b.image_url, b.release_date, b.language, b.media_type,
			       b.created_at, b.updated_at
			FROM series_books sb
			JOIN books b ON b.id = sb.book_id
			WHERE sb.series_id = ? AND sb.position_in_series = ? AND b.status = 'wanted'`, sid, position)
		var b models.Book
		var monitored int
		var releaseDate sql.NullTime
		err := row.Scan(&b.ID, &b.ForeignID, &b.AuthorID, &b.Title, &b.SortTitle, &b.Status,
			&monitored, &b.ImageURL, &releaseDate, &b.Language, &b.MediaType, &b.CreatedAt, &b.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("series book scan: %w", err)
		}
		b.Monitored = monitored == 1
		if releaseDate.Valid {
			b.ReleaseDate = &releaseDate.Time
		}
		found = append(found, &b)
	}
	if len(found) != 1 {
		return nil, nil
	}
	return found[0], nil
}
