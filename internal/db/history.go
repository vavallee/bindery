package db

import (
	"context"
	"database/sql"
	"errors"
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

// ListForUser returns history events whose associated book is owned by
// userID. Events with NULL book_id are included regardless of user — they're
// orphan grab/failure records from before a book was linked and have no
// owner to scope to. When userID == 0 this falls back to List.
func (r *HistoryRepo) ListForUser(ctx context.Context, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.List(ctx)
	}
	q := "SELECT " + historyColumns + ` FROM history
		WHERE book_id IS NULL
		   OR book_id IN (SELECT id FROM books WHERE owner_user_id = ?)
		ORDER BY created_at DESC`
	return r.query(ctx, q, userID)
}

// ListByBookAndUser is ListByBook filtered to the user's library — the route
// already takes a bookId, but the user could supply someone else's id and
// we'd happily list their grab/import history. The book lookup gates it.
func (r *HistoryRepo) ListByBookAndUser(ctx context.Context, bookID, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.ListByBook(ctx, bookID)
	}
	q := "SELECT " + historyColumns + ` FROM history
		WHERE book_id = ?
		  AND book_id IN (SELECT id FROM books WHERE owner_user_id = ?)
		ORDER BY created_at DESC`
	return r.query(ctx, q, bookID, userID)
}

// ListByTypeAndUser is ListByType filtered to events whose book is owned by
// userID. Mirrors ListForUser's treatment of NULL book_id (legacy/orphan
// events pass through).
func (r *HistoryRepo) ListByTypeAndUser(ctx context.Context, eventType string, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.ListByType(ctx, eventType)
	}
	q := "SELECT " + historyColumns + ` FROM history
		WHERE event_type = ?
		  AND (book_id IS NULL
		       OR book_id IN (SELECT id FROM books WHERE owner_user_id = ?))
		ORDER BY created_at DESC`
	return r.query(ctx, q, eventType, userID)
}

// GetOwnerByID resolves the owner of a history event by joining through the
// referenced book. Returns:
//   - (owner, true, nil) when the event exists and its book has an owner;
//   - (0, true, nil) when the event exists but book_id is NULL or the book
//     row has no owner (legacy/orphan event — auth.CheckOwnership passes
//     these through);
//   - (0, false, nil) when the event id does not exist.
//
// The shape mirrors DownloadRepo.GetOwnerByID so handlers can treat all three
// tier-2 resources the same way.
func (r *HistoryRepo) GetOwnerByID(ctx context.Context, id int64) (int64, bool, error) {
	var owner sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT b.owner_user_id
		  FROM history h
		  LEFT JOIN books b ON b.id = h.book_id
		 WHERE h.id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get history owner: %w", err)
	}
	return owner.Int64, true, nil
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
