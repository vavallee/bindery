package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// EditionRepo owns the editions table. Bindery's pre-v0.8.1 code wrote
// editions only via the metadata aggregator ingesting a fresh book; the
// Calibre library importer is the first caller that needs to create and
// upsert editions from outside that flow, so we now expose a dedicated
// repo rather than leaking SQL into the importer.
type EditionRepo struct {
	db   *sql.DB
	exec dbExecutor
}

func NewEditionRepo(db *sql.DB) *EditionRepo {
	return &EditionRepo{db: db, exec: db}
}

// WithTx returns a clone of this repo with its tx-aware methods (Delete)
// routed through tx. See dbExecutor for the rationale.
func (r *EditionRepo) WithTx(tx *sql.Tx) *EditionRepo {
	clone := *r
	clone.exec = tx
	return &clone
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
	e, err := scanEditionFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get edition %s: %w", foreignID, err)
	}
	return &e, nil
}

// ListByBook returns every edition linked to bookID, in insertion order.
func (r *EditionRepo) ListByBook(ctx context.Context, bookID int64) ([]models.Edition, error) {
	rows, err := r.exec.QueryContext(ctx, `
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
		e, err := scanEditionFrom(rows)
		if err != nil {
			return nil, fmt.Errorf("scan edition: %w", err)
		}
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
		e.Publisher, timeArg(e.PublishDate), e.Format, e.NumPages, e.Language,
		e.ImageURL, isEbook, e.EditionInfo, monitored, timeValueArg(now), timeValueArg(now))
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

// UpsertMetadata inserts an edition discovered from an external metadata
// provider, or fills empty fields on the existing row for the same book. On a
// successful insert or update, e is hydrated with the stored row so callers
// that promote edition fields use the persisted values. It deliberately refuses
// to re-parent a foreign edition ID that already belongs to another book;
// callers can treat ok=false as a benign skipped conflict.
func (r *EditionRepo) UpsertMetadata(ctx context.Context, e *models.Edition) (bool, error) {
	if e == nil || strings.TrimSpace(e.ForeignID) == "" || e.BookID == 0 {
		return false, nil
	}
	if strings.TrimSpace(e.Title) == "" {
		e.Title = "Unknown Edition"
	}

	now := time.Now().UTC()
	isEbook := 0
	if e.IsEbook {
		isEbook = 1
	}
	monitored := 1

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO editions (foreign_id, book_id, title, isbn_13, isbn_10, asin,
		                      publisher, publish_date, format, num_pages, language,
		                      image_url, is_ebook, edition_info, monitored,
		                      created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(foreign_id) DO UPDATE SET
		    title       = COALESCE(NULLIF(editions.title, ''), excluded.title),
		    isbn_13     = COALESCE(NULLIF(editions.isbn_13, ''), excluded.isbn_13),
		    isbn_10     = COALESCE(NULLIF(editions.isbn_10, ''), excluded.isbn_10),
		    asin        = COALESCE(NULLIF(editions.asin, ''), excluded.asin),
		    publisher   = COALESCE(NULLIF(editions.publisher, ''), excluded.publisher),
		    publish_date= COALESCE(editions.publish_date, excluded.publish_date),
		    format      = COALESCE(NULLIF(editions.format, ''), excluded.format),
		    num_pages   = COALESCE(editions.num_pages, excluded.num_pages),
		    language    = COALESCE(NULLIF(editions.language, ''), excluded.language),
		    image_url   = COALESCE(NULLIF(editions.image_url, ''), excluded.image_url),
		    is_ebook    = CASE WHEN editions.is_ebook = 1 THEN 1 ELSE excluded.is_ebook END,
		    edition_info= COALESCE(NULLIF(editions.edition_info, ''), excluded.edition_info),
		    updated_at  = excluded.updated_at
		WHERE editions.book_id = excluded.book_id`,
		e.ForeignID, e.BookID, e.Title, e.ISBN13, e.ISBN10, e.ASIN,
		e.Publisher, timeArg(e.PublishDate), e.Format, e.NumPages, e.Language,
		e.ImageURL, isEbook, e.EditionInfo, monitored, timeValueArg(now), timeValueArg(now))
	if err != nil {
		return false, fmt.Errorf("upsert metadata edition %s: %w", e.ForeignID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("metadata edition rows affected %s: %w", e.ForeignID, err)
	}
	if affected == 0 {
		return false, nil
	}

	row := r.db.QueryRowContext(ctx, `
		SELECT id, foreign_id, book_id, title, isbn_13, isbn_10, asin, publisher,
		       publish_date, format, num_pages, language, image_url, is_ebook,
		       edition_info, monitored, created_at, updated_at
		FROM editions WHERE foreign_id = ?`, e.ForeignID)
	stored, err := scanEditionFrom(row)
	if err != nil {
		return false, fmt.Errorf("lookup metadata edition %s: %w", e.ForeignID, err)
	}
	if stored.BookID != e.BookID {
		e.ID = 0
		return false, nil
	}
	*e = stored
	return true, nil
}

// scanEditionFrom decodes an editions row using parseFlexibleTime for the
// three time-typed columns so legacy rows written by Go's default
// time.String shape, or by Calibre's own pubdate writer, still Scan
// successfully (#914 root cause).
func scanEditionFrom(s rowScanner) (models.Edition, error) {
	var e models.Edition
	var isEbook, monitored int
	var publishDateStr, createdAtStr, updatedAtStr sql.NullString
	if err := s.Scan(&e.ID, &e.ForeignID, &e.BookID, &e.Title, &e.ISBN13, &e.ISBN10,
		&e.ASIN, &e.Publisher, &publishDateStr, &e.Format, &e.NumPages, &e.Language,
		&e.ImageURL, &isEbook, &e.EditionInfo, &monitored, &createdAtStr, &updatedAtStr); err != nil {
		return e, err
	}
	if pd, perr := parseFlexibleTime(publishDateStr); perr != nil {
		slog.Warn("unparseable edition.publish_date, leaving nil", "edition_id", e.ID, "value", publishDateStr.String, "error", perr)
	} else {
		e.PublishDate = pd
	}
	e.CreatedAt = parseFlexibleTimeValue(createdAtStr, "editions.created_at")
	e.UpdatedAt = parseFlexibleTimeValue(updatedAtStr, "editions.updated_at")
	e.IsEbook = isEbook == 1
	e.Monitored = monitored == 1
	return e, nil
}

func (r *EditionRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.exec.ExecContext(ctx, `DELETE FROM editions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete edition %d: %w", id, err)
	}
	return nil
}
