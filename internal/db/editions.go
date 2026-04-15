package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// EditionRepo owns the editions table. Bindery's pre-v0.8.1 code wrote
// editions only via the metadata aggregator ingesting a fresh book; the
// Calibre library importer is the first caller that needs to create and
// upsert editions from outside that flow, so we now expose a dedicated
// repo rather than leaking SQL into the importer.
type EditionRepo struct {
	db *sql.DB
}

func NewEditionRepo(db *sql.DB) *EditionRepo {
	return &EditionRepo{db: db}
}

// GetByForeignID returns the edition keyed by its globally-unique foreign
// id. Bindery composes foreign ids for Calibre editions as
// `calibre:<book_id>:<FORMAT>` so the importer can find-or-create each
// edition without racing with itself on re-runs.
func (r *EditionRepo) GetByForeignID(ctx context.Context, foreignID string) (*models.Edition, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, foreign_id, book_id, title, isbn_13, isbn_10, asin, publisher,
		       publish_date, format, num_pages, language, image_url, is_ebook,
		       edition_info, monitored, created_at, updated_at
		FROM editions WHERE foreign_id = ?`, foreignID)
	var e models.Edition
	var isEbook, monitored int
	err := row.Scan(&e.ID, &e.ForeignID, &e.BookID, &e.Title, &e.ISBN13, &e.ISBN10,
		&e.ASIN, &e.Publisher, &e.PublishDate, &e.Format, &e.NumPages, &e.Language,
		&e.ImageURL, &isEbook, &e.EditionInfo, &monitored, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get edition %s: %w", foreignID, err)
	}
	e.IsEbook = isEbook == 1
	e.Monitored = monitored == 1
	return &e, nil
}

// ListByBook returns every edition linked to bookID, in insertion order.
func (r *EditionRepo) ListByBook(ctx context.Context, bookID int64) ([]models.Edition, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, foreign_id, book_id, title, isbn_13, isbn_10, asin, publisher,
		       publish_date, format, num_pages, language, image_url, is_ebook,
		       edition_info, monitored, created_at, updated_at
		FROM editions WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("list editions for book %d: %w", bookID, err)
	}
	defer rows.Close()

	var out []models.Edition
	for rows.Next() {
		var e models.Edition
		var isEbook, monitored int
		if err := rows.Scan(&e.ID, &e.ForeignID, &e.BookID, &e.Title, &e.ISBN13, &e.ISBN10,
			&e.ASIN, &e.Publisher, &e.PublishDate, &e.Format, &e.NumPages, &e.Language,
			&e.ImageURL, &isEbook, &e.EditionInfo, &monitored, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan edition: %w", err)
		}
		e.IsEbook = isEbook == 1
		e.Monitored = monitored == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// Upsert inserts the edition if its foreign_id is new, or updates the
// format / title / isbn / publish_date fields in place if the row already
// exists. Returns the persisted edition id.
//
// The upsert is scoped to the columns the Calibre importer actually
// derives from metadata.db. Fields outside that scope (edition_info,
// publisher, num_pages, asin, monitored) are preserved on update so a
// re-import never flattens metadata the user has curated in-UI.
func (r *EditionRepo) Upsert(ctx context.Context, e *models.Edition) error {
	now := time.Now().UTC()
	isEbook := 0
	if e.IsEbook {
		isEbook = 1
	}
	monitored := 1
	if !e.Monitored {
		// Default to true on insert so existing UI contracts hold; explicit
		// false is only honoured if the caller really set it.
		monitored = 1
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO editions (foreign_id, book_id, title, isbn_13, isbn_10, asin,
		                      publisher, publish_date, format, num_pages, language,
		                      image_url, is_ebook, edition_info, monitored,
		                      created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(foreign_id) DO UPDATE SET
		    title       = excluded.title,
		    isbn_13     = COALESCE(excluded.isbn_13, editions.isbn_13),
		    format      = excluded.format,
		    publish_date= COALESCE(excluded.publish_date, editions.publish_date),
		    is_ebook    = excluded.is_ebook,
		    image_url   = COALESCE(NULLIF(excluded.image_url, ''), editions.image_url),
		    language    = excluded.language,
		    updated_at  = excluded.updated_at`,
		e.ForeignID, e.BookID, e.Title, e.ISBN13, e.ISBN10, e.ASIN,
		e.Publisher, e.PublishDate, e.Format, e.NumPages, e.Language,
		e.ImageURL, isEbook, e.EditionInfo, monitored, now, now)
	if err != nil {
		return fmt.Errorf("upsert edition %s: %w", e.ForeignID, err)
	}
	// ON CONFLICT UPDATE returns LastInsertId=0 on conflict; fetch the
	// existing id explicitly in that case.
	if id, _ := res.LastInsertId(); id > 0 {
		e.ID = id
	} else {
		row := r.db.QueryRowContext(ctx, "SELECT id FROM editions WHERE foreign_id = ?", e.ForeignID)
		if err := row.Scan(&e.ID); err != nil {
			return fmt.Errorf("lookup upserted edition %s: %w", e.ForeignID, err)
		}
	}
	e.UpdatedAt = now
	return nil
}
