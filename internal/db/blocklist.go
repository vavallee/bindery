package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type BlocklistRepo struct {
	db *sql.DB
}

func NewBlocklistRepo(db *sql.DB) *BlocklistRepo {
	return &BlocklistRepo{db: db}
}

func (r *BlocklistRepo) Create(ctx context.Context, e *models.BlocklistEntry) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO blocklist (book_id, guid, title, indexer_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.BookID, e.GUID, e.Title, e.IndexerID, e.Reason, now)
	if err != nil {
		return fmt.Errorf("create blocklist entry: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get blocklist entry id: %w", err)
	}
	e.ID = id
	e.CreatedAt = now
	return nil
}

func (r *BlocklistRepo) List(ctx context.Context) ([]models.BlocklistEntry, error) {
	return r.query(ctx, "SELECT id, book_id, guid, title, indexer_id, reason, created_at FROM blocklist ORDER BY created_at DESC")
}

func (r *BlocklistRepo) IsBlocked(ctx context.Context, guid string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM blocklist WHERE guid=?", guid).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check blocklist: %w", err)
	}
	return count > 0, nil
}

func (r *BlocklistRepo) DeleteByID(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM blocklist WHERE id=?", id)
	return err
}

func (r *BlocklistRepo) DeleteByBookID(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM blocklist WHERE book_id=?", bookID)
	return err
}

func (r *BlocklistRepo) query(ctx context.Context, q string, args ...interface{}) ([]models.BlocklistEntry, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.BlocklistEntry
	for rows.Next() {
		var e models.BlocklistEntry
		if err := rows.Scan(
			&e.ID, &e.BookID, &e.GUID, &e.Title, &e.IndexerID, &e.Reason, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan blocklist entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
