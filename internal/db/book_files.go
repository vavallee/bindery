package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// BookFileRepo manages the book_files table.
type BookFileRepo struct {
	db *sql.DB
}

// NewBookFileRepo creates a new BookFileRepo backed by the given database.
func NewBookFileRepo(db *sql.DB) *BookFileRepo {
	return &BookFileRepo{db: db}
}

// Add inserts a book_files row. Duplicate paths are silently ignored (INSERT OR IGNORE).
func (r *BookFileRepo) Add(ctx context.Context, bookID int64, format, path string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO book_files (book_id, format, path, size_bytes, created_at)
		 VALUES (?, ?, ?, 0, ?)`,
		bookID, format, path, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("book_files add: %w", err)
	}
	return nil
}

// UpdatePath changes the on-disk path of the book_files row with the given id.
// Used by the library reorganize action (#1181) after a tracked file is moved
// to the location the current naming template computes. The path column is
// globally UNIQUE, so a move onto a path another row already owns fails here
// rather than silently corrupting the index.
func (r *BookFileRepo) UpdatePath(ctx context.Context, id int64, newPath string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE book_files SET path = ? WHERE id = ?`, newPath, id)
	if err != nil {
		return fmt.Errorf("book_files update path: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("book_files update path rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("book_files update path: no row with id %d", id)
	}
	return nil
}

// BookIDForFile returns the owning book_id for a book_files row id, or 0 when
// no such row exists. Used by the reorganize apply path (#1181) to resolve a
// file the client picked back to its book.
func (r *BookFileRepo) BookIDForFile(ctx context.Context, fileID int64) (int64, error) {
	var bookID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT book_id FROM book_files WHERE id = ?`, fileID).Scan(&bookID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("book_files book_id lookup: %w", err)
	}
	return bookID, nil
}

// ListByBook returns all book_files rows for the given book, ordered by id.
func (r *BookFileRepo) ListByBook(ctx context.Context, bookID int64) ([]models.BookFile, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, book_id, format, path, size_bytes, created_at
		 FROM book_files WHERE book_id = ? ORDER BY id`,
		bookID)
	if err != nil {
		return nil, fmt.Errorf("book_files list: %w", err)
	}
	defer rows.Close()

	var files []models.BookFile
	for rows.Next() {
		var f models.BookFile
		if err := rows.Scan(&f.ID, &f.BookID, &f.Format, &f.Path, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("book_files scan: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// DeleteByBook removes all book_files rows for the given book.
func (r *BookFileRepo) DeleteByBook(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM book_files WHERE book_id = ?`, bookID)
	if err != nil {
		return fmt.Errorf("book_files delete by book: %w", err)
	}
	return nil
}

// DeleteByPath removes the book_files row matching the given path and returns
// the book_id of the deleted row (0 if no row matched).
func (r *BookFileRepo) DeleteByPath(ctx context.Context, path string) (int64, error) {
	var bookID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT book_id FROM book_files WHERE path = ?`, path).Scan(&bookID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("book_files lookup by path: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM book_files WHERE path = ?`, path); err != nil {
		return 0, fmt.Errorf("book_files delete by path: %w", err)
	}
	return bookID, nil
}

// PathOwnedByOtherBook reports whether the given on-disk path is present in
// book_files under a book other than excludeBookID. The path column is globally
// UNIQUE, so there is at most one owner. Pass excludeBookID=0 to treat ANY
// registered owner as "another book" (e.g. when the current book's rows have
// already been cascade-deleted). The delete and reassign-cleanup paths use this
// to avoid os.Remove-ing a file another book still owns (#1368).
func (r *BookFileRepo) PathOwnedByOtherBook(ctx context.Context, path string, excludeBookID int64) (bool, error) {
	var owner int64
	err := r.db.QueryRowContext(ctx, `SELECT book_id FROM book_files WHERE path = ? LIMIT 1`, path).Scan(&owner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("book_files owner lookup: %w", err)
	}
	return owner != excludeBookID, nil
}

// ListAllPaths returns every path currently registered in book_files.
// Used by ScanLibrary to build the set of already-tracked files.
func (r *BookFileRepo) ListAllPaths(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT path FROM book_files`)
	if err != nil {
		return nil, fmt.Errorf("book_files list all paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("book_files scan path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
