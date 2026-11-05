package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// RejectImportInput is one Reject import action (#3250).
type RejectImportInput struct {
	BookID int64
	// EventID and EventCreatedAt identify the bookImported event being
	// rejected. A tracked row created after the event, or a path a later
	// import of the book names, belongs to that later import and is left
	// alone (superseded).
	EventID        int64
	EventCreatedAt time.Time
	// Paths are the library paths the import placed. Only those still tracked
	// against BookID are detached; every other file of the book stays.
	Paths []string
	// GUID is the source release. Empty when the import recorded none (a
	// manual import, or one from before #3248): nothing is blocklisted then.
	GUID  string
	Title string
	// Scope and Reason describe the blocklist entry written for GUID.
	Scope  string
	Reason string
	// UserID tags the blocklist row with who pressed the button; 0 leaves it
	// system attributed.
	UserID int64
	// OwnerUserID is the fallback owner of the history row (the book's owner
	// wins, see HistoryRepo.Create).
	OwnerUserID int64
	// HistoryData builds the data blob of the importRejected event from what
	// the transaction found. It runs inside the transaction.
	HistoryData func(*RejectImportResult) string
}

// RejectImportResult says what a reject did, so the caller can tell the user
// exactly what happened rather than what was meant to.
type RejectImportResult struct {
	// DetachedPaths are the book_files rows removed.
	DetachedPaths []string
	// SupersededPaths are tracked paths this import named that a newer
	// import of the book now holds. They are neither detached nor deleted.
	SupersededPaths []string
	// NoOp is true when nothing was detached: no blocklist entry, history
	// row or status change was written.
	NoOp bool
	// Blocklisted is true when this call wrote a blocklist entry;
	// AlreadyBlocklisted when an entry already blocked the release for the
	// book. Both false means there was no GUID to block.
	Blocklisted        bool
	AlreadyBlocklisted bool
	Status             string
}

// ErrImportAlreadyRejected is returned when an importRejected row already
// exists for the event.
var ErrImportAlreadyRejected = errors.New("this import was already rejected")

// rejectImportBeforeCommit is a test seam: a non nil error aborts the
// transaction after every write has been made, which is where a partial
// result would be left behind if the writes were not one unit.
var rejectImportBeforeCommit func() error

// RejectImport detaches the files an import placed, blocklists its source
// release and returns the book to Wanted, all in one transaction: either every
// row changes or none does. It never touches the disk; removing the file is
// the caller's job once this has committed.
func (r *BookRepo) RejectImport(ctx context.Context, in RejectImportInput) (*RejectImportResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("reject import: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var already int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history
		WHERE event_type = ? AND book_id = ? AND json_extract(data, '$.eventId') = ?`,
		models.HistoryEventImportRejected, in.BookID, in.EventID).Scan(&already); err != nil {
		return nil, fmt.Errorf("reject import: check earlier reject: %w", err)
	}
	if already > 0 {
		return nil, ErrImportAlreadyRejected
	}

	res := &RejectImportResult{}
	tracked, err := trackedBookPaths(ctx, tx, in.BookID)
	if err != nil {
		return nil, err
	}
	later, err := laterImportPaths(ctx, tx, in.BookID, in.EventID)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	for _, want := range in.Paths {
		key := filepath.Clean(want)
		row, ok := tracked[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		stored := row.path
		if later[key] || (!in.EventCreatedAt.IsZero() && row.createdAt.After(in.EventCreatedAt)) {
			res.SupersededPaths = append(res.SupersededPaths, stored)
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM book_files WHERE book_id = ? AND path = ?`, in.BookID, stored); err != nil {
			return nil, fmt.Errorf("reject import: detach file: %w", err)
		}
		res.DetachedPaths = append(res.DetachedPaths, stored)
	}

	if len(res.DetachedPaths) == 0 {
		// Nothing to reject: writing a blocklist entry or history row for
		// an import whose files are already gone or replaced would only
		// leave debris, so the action stops here.
		res.NoOp = true
		if err := tx.QueryRowContext(ctx, `SELECT status FROM books WHERE id = ?`, in.BookID).Scan(&res.Status); err != nil {
			return nil, fmt.Errorf("reject import: read status: %w", err)
		}
		return res, nil
	}

	if err := r.WithTx(tx).refreshBookStatus(ctx, in.BookID); err != nil {
		return nil, fmt.Errorf("reject import: refresh status: %w", err)
	}

	bookID := in.BookID
	if in.GUID != "" {
		forBook := &bookID
		if in.Scope == models.BlocklistScopeRelease {
			forBook = nil
		}
		blocked, err := blocklistBlocksBook(ctx, tx, in.GUID, forBook)
		if err != nil {
			return nil, err
		}
		if blocked {
			res.AlreadyBlocklisted = true
		} else {
			entry := &models.BlocklistEntry{BookID: &bookID, GUID: in.GUID, Title: in.Title, Reason: in.Reason, Scope: in.Scope}
			var uid *int64
			if in.UserID != 0 {
				u := in.UserID
				uid = &u
			}
			if err := insertBlocklistEntry(ctx, tx, entry, uid); err != nil {
				return nil, err
			}
			res.Blocklisted = true
		}
	}

	if err := tx.QueryRowContext(ctx, `SELECT status FROM books WHERE id = ?`, in.BookID).Scan(&res.Status); err != nil {
		return nil, fmt.Errorf("reject import: read status: %w", err)
	}

	data := ""
	if in.HistoryData != nil {
		data = in.HistoryData(res)
	}
	ev := &models.HistoryEvent{BookID: &bookID, EventType: models.HistoryEventImportRejected, SourceTitle: in.Title, Data: data, OwnerUserID: in.OwnerUserID}
	if err := insertHistoryEvent(ctx, tx, ev); err != nil {
		return nil, err
	}

	if rejectImportBeforeCommit != nil {
		if err := rejectImportBeforeCommit(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("reject import: commit: %w", err)
	}
	return res, nil
}

type trackedFile struct {
	path      string
	createdAt time.Time
}

// trackedBookPaths maps each cleaned path a book tracks to the row as stored.
func trackedBookPaths(ctx context.Context, exec dbExecutor, bookID int64) (map[string]trackedFile, error) {
	rows, err := exec.QueryContext(ctx, `SELECT path, created_at FROM book_files WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("reject import: list files: %w", err)
	}
	defer rows.Close()
	tracked := map[string]trackedFile{}
	for rows.Next() {
		var f trackedFile
		if err := rows.Scan(&f.path, &f.createdAt); err != nil {
			return nil, fmt.Errorf("reject import: scan file: %w", err)
		}
		tracked[filepath.Clean(f.path)] = f
	}
	return tracked, rows.Err()
}

// laterImportPaths is the set of cleaned paths named by the book's
// bookImported events newer than eventID. A re-import to the same path keeps
// the old book_files row (inserts are ignored), so the row's age cannot show
// that a newer import owns the file; the newer event can.
func laterImportPaths(ctx context.Context, exec dbExecutor, bookID, eventID int64) (map[string]bool, error) {
	rows, err := exec.QueryContext(ctx, `SELECT data FROM history WHERE book_id = ? AND event_type = ? AND id > ?`,
		bookID, models.HistoryEventBookImported, eventID)
	if err != nil {
		return nil, fmt.Errorf("reject import: list later imports: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var d struct {
			Path  string   `json:"path"`
			Paths []string `json:"paths"`
		}
		if json.Unmarshal([]byte(raw), &d) != nil {
			continue
		}
		if d.Path != "" {
			out[filepath.Clean(d.Path)] = true
		}
		for _, p := range d.Paths {
			out[filepath.Clean(p)] = true
		}
	}
	return out, rows.Err()
}

// RejectedImportEventIDs returns which of the given bookImported event ids
// have an importRejected row, so the UI can offer the action from server
// state.
func (r *HistoryRepo) RejectedImportEventIDs(ctx context.Context, eventIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(eventIDs) == 0 {
		return out, nil
	}
	want := map[int64]bool{}
	for _, id := range eventIDs {
		want[id] = true
	}
	rows, err := r.db.QueryContext(ctx, `SELECT data FROM history WHERE event_type = ?`, models.HistoryEventImportRejected)
	if err != nil {
		return nil, fmt.Errorf("list rejected imports: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var d struct {
			EventID int64 `json:"eventId"`
		}
		if json.Unmarshal([]byte(raw), &d) == nil && want[d.EventID] {
			out[d.EventID] = true
		}
	}
	return out, rows.Err()
}
