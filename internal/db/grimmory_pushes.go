package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"
)

// GrimmoryPushRepo records which files have already been pushed to Grimmory's
// BookDrop, keyed by on-disk path. See migration 059 for why this exists:
// BookDrop has no server-side dedup, so idempotency is Bindery's job.
type GrimmoryPushRepo struct {
	db *sql.DB
}

func NewGrimmoryPushRepo(db *sql.DB) *GrimmoryPushRepo {
	return &GrimmoryPushRepo{db: db}
}

// Record marks filePath as pushed for bookID. Re-recording the same path is a
// no-op (the first push wins), so callers don't need their own existence check
// between Has and Record.
func (r *GrimmoryPushRepo) Record(ctx context.Context, bookID int64, filePath string, grimmoryBookID int64) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO grimmory_pushes (book_id, file_path, grimmory_book_id) VALUES (?, ?, ?)
		 ON CONFLICT(file_path) DO NOTHING`,
		bookID, filepath.Clean(filePath), grimmoryBookID)
	return err
}

// Has reports whether filePath was already pushed.
func (r *GrimmoryPushRepo) Has(ctx context.Context, filePath string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM grimmory_pushes WHERE file_path = ?`, filepath.Clean(filePath)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LastPush returns when the most recent push happened and the total number of
// pushed files. A zero time with count 0 means nothing has been pushed yet.
func (r *GrimmoryPushRepo) LastPush(ctx context.Context) (time.Time, int, error) {
	var last sql.NullString
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(pushed_at), COUNT(*) FROM grimmory_pushes`).Scan(&last, &count)
	if err != nil {
		return time.Time{}, 0, err
	}
	t, err := parseFlexibleTime(last)
	if err != nil || t == nil {
		return time.Time{}, count, err
	}
	return *t, count, nil
}
