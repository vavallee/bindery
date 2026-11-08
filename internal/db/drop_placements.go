package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DropPlacementBook returns the book that last placed path in a drop folder, or
// 0 when no placement is on record: the file came from somewhere else, or the
// book that placed it has since been deleted and its row went with it (#3562).
func (r *BookRepo) DropPlacementBook(ctx context.Context, path string) (int64, error) {
	var bookID int64
	err := r.db.QueryRowContext(ctx, `SELECT book_id FROM drop_placements WHERE path = ?`, path).Scan(&bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("drop placement lookup: %w", err)
	}
	return bookID, nil
}

// RecordDropPlacement notes that bookID has just placed path in a drop folder,
// replacing whichever book placed a file there before.
func (r *BookRepo) RecordDropPlacement(ctx context.Context, path string, bookID int64) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO drop_placements (path, book_id) VALUES (?, ?)
		ON CONFLICT(path) DO UPDATE SET book_id = excluded.book_id`, path, bookID); err != nil {
		return fmt.Errorf("record drop placement: %w", err)
	}
	return nil
}
