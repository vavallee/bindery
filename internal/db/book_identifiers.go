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

// ErrBookIdentifierConflict indicates a book identifier is already owned by a
// different book row.
var ErrBookIdentifierConflict = errors.New("book identifier already belongs to another book")

// BookIdentifierConflictError reports which existing book owns the requested
// identifier. Mirrors AuthorIdentifierConflictError.
type BookIdentifierConflictError struct {
	ForeignID string
	BookID    int64
}

func (e *BookIdentifierConflictError) Error() string {
	if e == nil {
		return ErrBookIdentifierConflict.Error()
	}
	return fmt.Sprintf("book identifier %q already belongs to book %d", e.ForeignID, e.BookID)
}

func (e *BookIdentifierConflictError) Unwrap() error { return ErrBookIdentifierConflict }

// GetByAnyForeignID resolves a book by its primary foreign_id or by any
// provider identifier attached to it (#1705).
//
// This is the lookup that stops a second provider's view of a book becoming a
// second row. GetByForeignID matches books.foreign_id exactly, so an
// OpenLibrary work id can never find the row created from Hardcover, however
// obviously they are the same book.
//
// The primary column is checked first: it is the identity the row was created
// with, and on the overwhelmingly common path it hits without touching the
// join.
func (r *BookRepo) GetByAnyForeignID(ctx context.Context, foreignID string) (*models.Book, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	if book, err := r.GetByForeignID(ctx, foreignID); err != nil || book != nil {
		return book, err
	}
	books, err := r.query(ctx,
		bookCTE+" SELECT "+bookColumns+" FROM books "+bookJoins+
			" JOIN book_identifiers bi ON bi.book_id = books.id WHERE bi.foreign_id = ?",
		[]any{foreignID})
	if err != nil {
		return nil, err
	}
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// GetBookIdentifier returns the owner row for foreignID, or nil when unknown.
func (r *BookRepo) GetBookIdentifier(ctx context.Context, foreignID string) (*models.BookIdentifier, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	row := r.exec.QueryRowContext(ctx, `
		SELECT book_id, provider, foreign_id, created_at, updated_at
		FROM book_identifiers WHERE foreign_id = ?`, foreignID)
	var out models.BookIdentifier
	if err := row.Scan(&out.BookID, &out.Provider, &out.ForeignID, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get book identifier %q: %w", foreignID, err)
	}
	return &out, nil
}

// ListBookIdentifiers returns every provider id attached to a book.
func (r *BookRepo) ListBookIdentifiers(ctx context.Context, bookID int64) ([]models.BookIdentifier, error) {
	rows, err := r.exec.QueryContext(ctx, `
		SELECT book_id, provider, foreign_id, created_at, updated_at
		FROM book_identifiers WHERE book_id = ? ORDER BY provider, foreign_id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("list book identifiers %d: %w", bookID, err)
	}
	defer rows.Close()
	out := []models.BookIdentifier{}
	for rows.Next() {
		var identifier models.BookIdentifier
		if err := rows.Scan(&identifier.BookID, &identifier.Provider, &identifier.ForeignID,
			&identifier.CreatedAt, &identifier.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan book identifier: %w", err)
		}
		out = append(out, identifier)
	}
	return out, rows.Err()
}

// UpsertBookIdentifier attaches foreignID to bookID.
//
// Returns a *BookIdentifierConflictError when the id already belongs to a
// different book. That is a real condition rather than a corruption: two rows
// the catalogue believes are separate books can both claim one upstream id,
// and silently re-pointing it would move a book's identity out from under
// whoever is looking at it. The callers here treat it as "leave both alone and
// log", which keeps the failure visible without making it destructive.
func (r *BookRepo) UpsertBookIdentifier(ctx context.Context, bookID int64, foreignID string) error {
	return r.upsertBookIdentifierTx(ctx, r.exec, bookID, foreignID, time.Now().UTC())
}

// DeleteBookIdentifier detaches one id from a book.
func (r *BookRepo) DeleteBookIdentifier(ctx context.Context, bookID int64, foreignID string) error {
	foreignID = strings.TrimSpace(foreignID)
	if bookID == 0 || foreignID == "" {
		return nil
	}
	if _, err := r.exec.ExecContext(ctx, `
		DELETE FROM book_identifiers WHERE book_id = ? AND foreign_id = ?`, bookID, foreignID); err != nil {
		return fmt.Errorf("delete book identifier %q for book %d: %w", foreignID, bookID, err)
	}
	return nil
}

func (r *BookRepo) upsertBookIdentifierTx(ctx context.Context, exec dbExecutor, bookID int64, foreignID string, now time.Time) error {
	foreignID = strings.TrimSpace(foreignID)
	if bookID == 0 || foreignID == "" {
		return nil
	}
	provider := models.BookProviderFromForeignID(foreignID)
	result, err := exec.ExecContext(ctx, `
		INSERT INTO book_identifiers (book_id, provider, foreign_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(foreign_id) DO UPDATE SET
			provider = excluded.provider,
			updated_at = excluded.updated_at
		WHERE book_identifiers.book_id = excluded.book_id`,
		bookID, provider, foreignID, now, now)
	if err != nil {
		return fmt.Errorf("upsert book identifier %q: %w", foreignID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check book identifier %q: %w", foreignID, err)
	}
	if affected > 0 {
		return nil
	}
	// Zero rows affected means the conflict target matched but the guard
	// rejected the update, i.e. another book already owns this id.
	var ownerID int64
	row := exec.QueryRowContext(ctx, "SELECT book_id FROM book_identifiers WHERE foreign_id = ?", foreignID)
	if err := row.Scan(&ownerID); err != nil {
		return fmt.Errorf("read book identifier owner %q: %w", foreignID, err)
	}
	return &BookIdentifierConflictError{ForeignID: foreignID, BookID: ownerID}
}
