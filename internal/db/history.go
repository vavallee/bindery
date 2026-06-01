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

// historyColumns is the explicit column list for history SELECTs. It is kept
// in the exact order query() scans into models.HistoryEvent — changing the
// history schema means updating both this list and the Scan call in query().
const historyColumns = "id, book_id, event_type, source_title, data, created_at"

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
	return r.query(ctx, "SELECT "+historyColumns+" FROM history ORDER BY created_at DESC")
}

func (r *HistoryRepo) ListByBook(ctx context.Context, bookID int64) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT "+historyColumns+" FROM history WHERE book_id=? ORDER BY created_at DESC", bookID)
}

func (r *HistoryRepo) ListByType(ctx context.Context, eventType string) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT "+historyColumns+" FROM history WHERE event_type=? ORDER BY created_at DESC", eventType)
}

// HistoryListOpts is the filter+page argument for ListPage. Empty fields are
// treated as "no filter": a zero BookID and empty EventType return every
// row. Limit must be positive; Offset is clamped at 0 by ListPage.
type HistoryListOpts struct {
	BookID    int64
	EventType string
	Limit     int
	Offset    int
}

// ListPage returns one page of history events (newest first) plus the total
// row count that matches the filter. Backed by idx_history_created_at_desc
// (migration 047) so the newest-first scan does not pay an OrderBy step on
// top of a forward-only index walk.
func (r *HistoryRepo) ListPage(ctx context.Context, opts HistoryListOpts) ([]models.HistoryEvent, int, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	where := ""
	args := []any{}
	switch {
	case opts.BookID != 0:
		where = " WHERE book_id = ?"
		args = append(args, opts.BookID)
	case opts.EventType != "":
		where = " WHERE event_type = ?"
		args = append(args, opts.EventType)
	}

	var total int
	var countRow *sql.Row
	if len(args) > 0 {
		countRow = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM history"+where, args...)
	} else {
		countRow = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM history"+where)
	}
	if err := countRow.Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count history events: %w", err)
	}

	pageArgs := append([]any{}, args...)
	pageArgs = append(pageArgs, limit, offset)
	events, err := r.query(ctx,
		"SELECT "+historyColumns+" FROM history"+where+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
		pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (r *HistoryRepo) GetByID(ctx context.Context, id int64) (*models.HistoryEvent, error) {
	events, err := r.query(ctx, "SELECT "+historyColumns+" FROM history WHERE id=?", id)
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
