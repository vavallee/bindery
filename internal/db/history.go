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
	return insertHistoryEvent(ctx, r.db, e)
}

// insertHistoryEvent is Create over any executor, so a caller holding a
// transaction (RejectImport) writes the event in it.
func insertHistoryEvent(ctx context.Context, exec dbExecutor, e *models.HistoryEvent) error {
	now := time.Now().UTC()
	var fallbackOwner any
	if e.OwnerUserID != 0 {
		fallbackOwner = e.OwnerUserID
	}
	result, err := exec.ExecContext(ctx, `
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

// ReleaseFailureStreak counts one release's current run of counted failures
// (#4111), in order, from its history. It returns the count and when the last
// counted failure happened.
//
// Only downloadFailed events whose "class" is one of classes are considered
// (the filter is in SQL, so an event of another class cannot count whatever
// the caller does). The run is broken, and the count starts again, by:
//
//   - an import of the release, found by the guid on the event, or by title and
//     book for an old event that carries none;
//   - a grab that went through and was followed by neither a failure nor an
//     import (a second "grabbed" with nothing between). The grab right before a
//     "never arrived" failure is that failure's own attempt and breaks nothing.
//
// A failure within minGap of the last counted one is recorded but not counted,
// so a burst of manual searches cannot add up to a blocklist entry. Events
// older than since are ignored.
func (r *HistoryRepo) ReleaseFailureStreak(ctx context.Context, guid, title string, bookID *int64, since time.Time,
	classes []string, neverArrivedClass string, minGap time.Duration) (count int, lastCounted time.Time, pendingGrab bool, err error) {
	if len(classes) == 0 || guid == "" {
		return 0, time.Time{}, false, nil
	}
	marks := make([]string, len(classes))
	args := []any{since, models.HistoryEventDownloadFailed, guid}
	for i, c := range classes {
		marks[i] = "?"
		args = append(args, c)
	}
	args = append(args, models.HistoryEventGrabbed, guid, models.HistoryEventBookImported, guid, title, title, bookID)
	q := `SELECT event_type, COALESCE(json_extract(data, '$.class'), ''), created_at FROM history
		WHERE created_at > ? AND (
			(event_type = ? AND json_extract(data, '$.guid') = ? AND json_extract(data, '$.class') IN (` + strings.Join(marks, ",") + `))
			OR (event_type = ? AND json_extract(data, '$.guid') = ?)
			OR (event_type = ? AND (json_extract(data, '$.guid') = ?
				OR (COALESCE(json_extract(data, '$.guid'), '') = '' AND source_title = ? AND ? != '' AND book_id IS ?)))
		) ORDER BY created_at ASC, id ASC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("release failure streak: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, class string
		var at time.Time
		if err := rows.Scan(&typ, &class, &at); err != nil {
			return 0, time.Time{}, false, fmt.Errorf("release failure streak: %w", err)
		}
		switch typ {
		case models.HistoryEventBookImported:
			count, lastCounted, pendingGrab = 0, time.Time{}, false
		case models.HistoryEventGrabbed:
			if pendingGrab {
				count, lastCounted = 0, time.Time{}
			}
			pendingGrab = true
		default:
			if class != neverArrivedClass {
				// A failure that is not a never-arrived one was not preceded
				// by a grab of its own.
				if pendingGrab {
					count, lastCounted = 0, time.Time{}
				}
			}
			pendingGrab = false
			if lastCounted.IsZero() || at.Sub(lastCounted) >= minGap {
				count++
				lastCounted = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, time.Time{}, false, fmt.Errorf("release failure streak: %w", err)
	}
	return count, lastCounted, pendingGrab, nil
}

// IndexerFailuresElsewhere counts the downloadFailed events of the given class
// since `since` that name this indexer ("indexerId" on the event) on a release
// other than excludeGUID. It is the evidence that a failure is the indexer's
// and not the release's: when other releases from the same indexer fail the
// same way, the fault is indexer wide.
func (r *HistoryRepo) IndexerFailuresElsewhere(ctx context.Context, indexerID int64, excludeGUID, class string, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history
		WHERE event_type = ? AND created_at > ?
		AND json_extract(data, '$.class') = ?
		AND CAST(json_extract(data, '$.indexerId') AS TEXT) = ?
		AND COALESCE(json_extract(data, '$.guid'), '') != ?`,
		models.HistoryEventDownloadFailed, since, class, fmt.Sprint(indexerID), excludeGUID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("indexer failures elsewhere: %w", err)
	}
	return n, nil
}

// IndexerServedElsewhere reports whether a release other than excludeGUID was
// sent to a download client from this indexer ("indexerId" on the grabbed
// event) after `since`: the evidence that the indexer is otherwise serving.
func (r *HistoryRepo) IndexerServedElsewhere(ctx context.Context, indexerID int64, excludeGUID string, since time.Time) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history
		WHERE event_type = ? AND created_at > ?
		AND CAST(json_extract(data, '$.indexerId') AS TEXT) = ?
		AND COALESCE(json_extract(data, '$.guid'), '') != ?`,
		models.HistoryEventGrabbed, since, fmt.Sprint(indexerID), excludeGUID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("indexer served elsewhere: %w", err)
	}
	return n > 0, nil
}
