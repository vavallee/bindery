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

type HistoryRepo struct {
	db *sql.DB
}

func NewHistoryRepo(db *sql.DB) *HistoryRepo {
	return &HistoryRepo{db: db}
}

// historyColumns is the explicit column list for history SELECTs. It is kept
// in the exact order query() scans into models.HistoryEvent — changing the
// history schema means updating both this list and the Scan call in query().
const historyColumns = "id, book_id, event_type, source_title, data, created_at, COALESCE(owner_user_id, 0)"

// historyUserScope is the WHERE fragment that limits history to what a non
// admin may see under tenancy. It takes the user id twice.
//
// A row belongs to the user stamped on it (migration 117). A row with no
// stamped owner is shown while its book exists and is visible to the user,
// meaning the user owns it or it is shared. A row with neither an owner nor a
// book is shown to admins only: it is either a deleted book's history from
// before rows carried an owner, or a bookless event nobody can be tied to, and
// in both cases the release names and paths in it are someone's.
const historyUserScope = `(owner_user_id = ?
	OR (owner_user_id IS NULL
	    AND book_id IN (SELECT id FROM books WHERE owner_user_id = ? OR owner_user_id IS NULL)))`

// Create inserts e. The row's owner is the book's owner when the book has one,
// and otherwise e.OwnerUserID (the download's owner or the request identity),
// with 0 stored as NULL. Taking the book's owner here rather than from each
// caller means no write path can leave a book's history unattributed.
func (r *HistoryRepo) Create(ctx context.Context, e *models.HistoryEvent) error {
	now := time.Now().UTC()
	var fallbackOwner any
	if e.OwnerUserID != 0 {
		fallbackOwner = e.OwnerUserID
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO history (book_id, event_type, source_title, data, created_at, owner_user_id)
		VALUES (?, ?, ?, ?, ?, COALESCE((SELECT owner_user_id FROM books WHERE id = ?), ?))`,
		e.BookID, e.EventType, e.SourceTitle, e.Data, now, e.BookID, fallbackOwner)
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

// FirstEventAt returns the timestamp of the earliest history event of the
// given type, or nil when none exists. Used by the telemetry setup funnel
// to derive (and then pin) an install's first grab / first import.
//
// Deliberately ORDER BY + LIMIT 1 rather than MIN(created_at): an aggregate
// loses the column decltype, so the driver would hand back raw TEXT that a
// time scan rejects. A direct column read converts to time.Time normally,
// and the stored text form sorts chronologically.
func (r *HistoryRepo) FirstEventAt(ctx context.Context, eventType string) (*time.Time, error) {
	var at time.Time
	err := r.db.QueryRowContext(ctx,
		"SELECT created_at FROM history WHERE event_type=? ORDER BY created_at ASC LIMIT 1", eventType,
	).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("first %s event: %w", eventType, err)
	}
	t := at.UTC()
	return &t, nil
}

// HasEventOfType reports whether at least one history row has any of the given
// event types, stopping at the first match so it stays cheap on a large table.
func (r *HistoryRepo) HasEventOfType(ctx context.Context, eventTypes ...string) (bool, error) {
	if len(eventTypes) == 0 {
		return false, nil
	}
	args := make([]interface{}, len(eventTypes))
	marks := make([]string, len(eventTypes))
	for i, t := range eventTypes {
		args[i] = t
		marks[i] = "?"
	}
	var one int
	err := r.db.QueryRowContext(ctx,
		"SELECT 1 FROM history WHERE event_type IN ("+strings.Join(marks, ",")+") LIMIT 1", args...,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("history event lookup: %w", err)
	}
	return true, nil
}

func (r *HistoryRepo) ListByBook(ctx context.Context, bookID int64) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT "+historyColumns+" FROM history WHERE book_id=? ORDER BY created_at DESC", bookID)
}

func (r *HistoryRepo) ListByType(ctx context.Context, eventType string) ([]models.HistoryEvent, error) {
	return r.query(ctx, "SELECT "+historyColumns+" FROM history WHERE event_type=? ORDER BY created_at DESC", eventType)
}

// HistoryListOpts is the filter+page argument for ListPage. Empty fields are
// treated as "no filter": a zero BookID, empty EventType, and zero UserID
// return every row that survives the other filters. Limit must be positive;
// Offset is clamped at 0 by ListPage. UserID > 0 restricts to the events that
// user may see, see historyUserScope.
type HistoryListOpts struct {
	BookID    int64
	EventType string
	UserID    int64
	Limit     int
	Offset    int
}

// ListPage returns one page of history events (newest first) plus the total
// row count that matches the filter. Backed by idx_history_created_at_desc
// (migration 048) so the newest-first scan does not pay an OrderBy step on
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

	clauses := []string{}
	args := []any{}
	// Independent AND-combined filters: a request that sets both BookID and
	// EventType must match both. A switch here previously made them mutually
	// exclusive, silently dropping the event_type filter whenever a book_id was
	// also supplied.
	if opts.BookID != 0 {
		clauses = append(clauses, "book_id = ?")
		args = append(args, opts.BookID)
	}
	if opts.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, opts.EventType)
	}
	if opts.UserID > 0 {
		clauses = append(clauses, historyUserScope)
		args = append(args, opts.UserID, opts.UserID)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
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

// ListForUser returns the history events userID may see (historyUserScope).
// When userID == 0 this falls back to List.
func (r *HistoryRepo) ListForUser(ctx context.Context, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.List(ctx)
	}
	q := "SELECT " + historyColumns + " FROM history WHERE " + historyUserScope + " ORDER BY created_at DESC"
	return r.query(ctx, q, userID, userID)
}

// ListByBookAndUser is ListByBook filtered to the user's library — the route
// already takes a bookId, but the user could supply someone else's id and
// we'd happily list their grab/import history. The book lookup gates it.
func (r *HistoryRepo) ListByBookAndUser(ctx context.Context, bookID, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.ListByBook(ctx, bookID)
	}
	q := "SELECT " + historyColumns + " FROM history WHERE book_id = ? AND " + historyUserScope + " ORDER BY created_at DESC"
	return r.query(ctx, q, bookID, userID, userID)
}

// ListByTypeAndUser is ListByType filtered to the events userID may see
// (historyUserScope).
func (r *HistoryRepo) ListByTypeAndUser(ctx context.Context, eventType string, userID int64) ([]models.HistoryEvent, error) {
	if userID == 0 {
		return r.ListByType(ctx, eventType)
	}
	q := "SELECT " + historyColumns + " FROM history WHERE event_type = ? AND " + historyUserScope + " ORDER BY created_at DESC"
	return r.query(ctx, q, eventType, userID, userID)
}

// GetOwnerByID resolves the owner of a history event: the owner stamped on the
// row, or for a row with none, the current owner of its book. Returns:
//   - (owner, true, nil) when the event exists and has an owner;
//   - (0, true, nil) when the event exists with no owner either way. Such a
//     row is not any user's to act on; HistoryHandler leaves it to admins
//     under tenancy;
//   - (0, false, nil) when the event id does not exist.
//
// The shape mirrors DownloadRepo.GetOwnerByID so handlers can treat all three
// tier-2 resources the same way.
func (r *HistoryRepo) GetOwnerByID(ctx context.Context, id int64) (int64, bool, error) {
	var owner sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(h.owner_user_id, b.owner_user_id)
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
			&e.ID, &e.BookID, &e.EventType, &e.SourceTitle, &e.Data, &e.CreatedAt, &e.OwnerUserID,
		); err != nil {
			return nil, fmt.Errorf("scan history event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
