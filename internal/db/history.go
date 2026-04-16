package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type HistoryRepo struct {
	db *sql.DB
}

func NewHistoryRepo(db *sql.DB) *HistoryRepo {
	return &HistoryRepo{db: db}
}

func (r *HistoryRepo) Create(ctx context.Context, e *models.HistoryEvent) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO history (book_id, event_type, source_title, data, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		e.BookID, e.EventType, e.SourceTitle, e.Data, now)
	if err != nil {
		return fmt.Errorf("create history event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get history event id: %w", err)
	}
	e.ID = id
	e.CreatedAt = now
	return nil
}

func (r *HistoryRepo) List(ctx context.Context) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT * FROM history ORDER BY created_at DESC")
}

func (r *HistoryRepo) ListByBook(ctx context.Context, bookID int64) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT * FROM history WHERE book_id=? ORDER BY created_at DESC", bookID)
}

func (r *HistoryRepo) ListByType(ctx context.Context, eventType string) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT * FROM history WHERE event_type=? ORDER BY created_at DESC", eventType)
}

func (r *HistoryRepo) GetByID(ctx context.Context, id int64) (*models.HistoryEvent, error) {
	events, err := r.query(ctx, "SELECT * FROM history WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return &events[0], nil
}

func (r *HistoryRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM history WHERE id=?", id)
	return err
}

func (r *HistoryRepo) query(ctx context.Context, q string, args ...interface{}) ([]models.HistoryEvent, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []models.HistoryEvent
	for rows.Next() {
		var e models.HistoryEvent
		if err := rows.Scan(
			&e.ID, &e.BookID, &e.EventType, &e.SourceTitle, &e.Data, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan history event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
