package db

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/vavallee/bindery/internal/models"
)

// RejectImportInput is one Reject import action (#3250).
type RejectImportInput struct {
	BookID int64
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
	// Blocklisted is true when this call wrote a blocklist entry;
	// AlreadyBlocklisted when an entry already blocked the release for the
	// book. Both false means there was no GUID to block.
	Blocklisted        bool
	AlreadyBlocklisted bool
	Status             string
}

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

	res := &RejectImportResult{}
	tracked, err := trackedBookPaths(ctx, tx, in.BookID)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	for _, want := range in.Paths {
		key := filepath.Clean(want)
		stored, ok := tracked[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.ExecContext(ctx, `DELETE FROM book_files WHERE book_id = ? AND path = ?`, in.BookID, stored); err != nil {
			return nil, fmt.Errorf("reject import: detach file: %w", err)
		}
		res.DetachedPaths = append(res.DetachedPaths, stored)
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

// trackedBookPaths maps each cleaned path a book tracks to the path as stored.
func trackedBookPaths(ctx context.Context, exec dbExecutor, bookID int64) (map[string]string, error) {
	rows, err := exec.QueryContext(ctx, `SELECT path FROM book_files WHERE book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, fmt.Errorf("reject import: list files: %w", err)
	}
	defer rows.Close()
	tracked := map[string]string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("reject import: scan file: %w", err)
		}
		tracked[filepath.Clean(p)] = p
	}
	return tracked, rows.Err()
}
