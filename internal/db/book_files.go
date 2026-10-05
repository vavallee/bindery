package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// BookFileRepo manages the book_files table.
type BookFileRepo struct {
	db *sql.DB
}

// NewBookFileRepo creates a new BookFileRepo backed by the given database.
func NewBookFileRepo(db *sql.DB) *BookFileRepo {
	return &BookFileRepo{db: db}
}

// Fingerprint reads a cheap snapshot of book_files — its row count and its
// highest id — for a cache keyed on the table's actual contents rather than a
// counter this repo maintains itself (the manual-import scan's tracked-file
// index, #2480).
//
// An earlier version of that cache was keyed on an atomic counter bumped by
// this repo's own mutating methods, which went stale: book_files rows also
// disappear through the books(id) ON DELETE CASCADE FK when a book or author
// is deleted (BookRepo.Delete, AuthorRepo.Delete), and through
// BookRepo.UntrackFilePath's rollback DELETE — neither goes through this repo,
// so neither could bump its counter. Reading count+maxID directly from the
// table instead catches every mutation regardless of which code path made it:
// a row removed without a matching insert changes the count, and any insert
// hands out a strictly larger AUTOINCREMENT id, so the pair only repeats when
// nothing actually changed. The query is a single indexed aggregate, cheap
// enough to run on every scan request.
func (r *BookFileRepo) Fingerprint(ctx context.Context) (count int64, maxID int64, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(id), 0) FROM book_files`).Scan(&count, &maxID)
	if err != nil {
		return 0, 0, fmt.Errorf("book_files fingerprint: %w", err)
	}
	return count, maxID, nil
}

// pathEpochs holds one counter per database, bumped by every in place rewrite
// of book_files.path (UpdatePath). Fingerprint's (count, maxID) pair catches
// inserts and deletes however they happen, including the FK cascade, but an
// UPDATE changes neither number, so the manual-import scan's tracked-file
// cache (#2480) keys on this as well. It is keyed by *sql.DB rather than held
// on the repo because production builds more than one BookFileRepo over the
// same database (BookRepo owns one, the importer wiring another), and the
// rename has to be visible to every reader regardless of which one wrote it.
var pathEpochs sync.Map // *sql.DB -> *atomic.Uint64

func pathEpochFor(db *sql.DB) *atomic.Uint64 {
	v, _ := pathEpochs.LoadOrStore(db, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

// PathEpoch returns the in place path rewrite counter for this database. Read
// it before Fingerprint and the rows a cache is built from: a rename that
// lands mid rebuild then shows up as a changed epoch on the next read, so the
// cache errs toward rebuilding, never toward serving a stale path.
func (r *BookFileRepo) PathEpoch() uint64 {
	return pathEpochFor(r.db).Load()
}

// PathOwnedError reports that a path is already tracked in book_files by a
// different, existing book. book_files.path is globally UNIQUE, so the row
// cannot be recorded for a second book; before #2937 the insert was a silent
// OR IGNORE that left the caller believing it had tracked the file. The owner
// is named so the message tells the user where the file actually is.
type PathOwnedError struct {
	Path        string
	OwnerBookID int64
	OwnerTitle  string
}

func (e *PathOwnedError) Error() string {
	return fmt.Sprintf("%s is already tracked on another book, %q (id %d)", e.Path, e.OwnerTitle, e.OwnerBookID)
}

// TrackOutcome says what Track did with a path.
type TrackOutcome int

const (
	// TrackInserted means a new row was written for the book.
	TrackInserted TrackOutcome = iota + 1
	// TrackAlreadyTracked means the same book already held the path, so the
	// call was an idempotent no-op (retries and rescans rely on this).
	TrackAlreadyTracked
	// TrackReclaimedOrphan means the path was held by a row whose book no
	// longer exists (foreign_keys lost, #1727). That row is stale, so it was
	// re-pointed at the book.
	TrackReclaimedOrphan
)

// TrackResult is the outcome of Track. PreviousBookID is the dead book id an
// orphaned row named before it was reclaimed.
type TrackResult struct {
	Outcome        TrackOutcome
	PreviousBookID int64
}

// Created reports whether this call made the row the book's: a fresh insert
// or a reclaimed orphan. A same-book no-op is not a creation.
func (r TrackResult) Created() bool {
	return r.Outcome == TrackInserted || r.Outcome == TrackReclaimedOrphan
}

// Track records path against bookID and reports what happened. A path the
// same book already tracks is a no-op success, a row left behind by a deleted
// book is taken over, and a path another existing book tracks fails with
// *PathOwnedError and leaves that book's row alone (#2937).
//
// The insert and the ownership lookup run in one transaction so the answer
// describes the row this call saw, not one a concurrent writer changed in
// between. Nothing else happens inside it: no file I/O, no other query on
// r.db (the pool has one connection).
func (r *BookFileRepo) Track(ctx context.Context, bookID int64, format, path string) (TrackResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return TrackResult{}, fmt.Errorf("book_files track begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO book_files (book_id, format, path, size_bytes, created_at)
		 VALUES (?, ?, ?, 0, ?)`,
		bookID, format, path, time.Now().UTC())
	if err != nil {
		return TrackResult{}, fmt.Errorf("book_files add: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return TrackResult{}, fmt.Errorf("book_files add rows: %w", err)
	}
	if n > 0 {
		if err := tx.Commit(); err != nil {
			return TrackResult{}, fmt.Errorf("book_files add commit: %w", err)
		}
		return TrackResult{Outcome: TrackInserted}, nil
	}

	var owner int64
	var ownerTitle sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT bf.book_id, b.title FROM book_files bf LEFT JOIN books b ON b.id = bf.book_id
		 WHERE bf.path = ?`, path).Scan(&owner, &ownerTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackResult{}, fmt.Errorf("book_files add: insert of %s was ignored but no row holds the path", path)
	}
	if err != nil {
		return TrackResult{}, fmt.Errorf("book_files owner lookup: %w", err)
	}
	switch {
	case owner == bookID:
		return TrackResult{Outcome: TrackAlreadyTracked}, nil
	case !ownerTitle.Valid:
		if _, err := tx.ExecContext(ctx,
			`UPDATE book_files SET book_id = ?, format = ? WHERE path = ?`, bookID, format, path); err != nil {
			return TrackResult{}, fmt.Errorf("book_files reclaim orphaned row: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return TrackResult{}, fmt.Errorf("book_files reclaim commit: %w", err)
		}
		return TrackResult{Outcome: TrackReclaimedOrphan, PreviousBookID: owner}, nil
	default:
		return TrackResult{}, &PathOwnedError{Path: path, OwnerBookID: owner, OwnerTitle: ownerTitle.String}
	}
}

// Add records path against bookID. It is Track without the outcome: a
// same-book re-add succeeds, and a path another book owns returns
// *PathOwnedError instead of being silently ignored (#2937).
func (r *BookFileRepo) Add(ctx context.Context, bookID int64, format, path string) error {
	_, err := r.Track(ctx, bookID, format, path)
	return err
}

// AddIfMissing records a new on-disk file and reports whether this call is the
// one that made the row the book's (see TrackResult.Created). A path the book
// already tracks comes back false; a path another book owns comes back as
// *PathOwnedError rather than being stolen or reported as handled.
//
// Callers that need to know what they own use this instead of Add: the Calibre
// importer may only claim, and later roll back, a file row it actually created
// (#1635), mirroring SeriesRepo.LinkBookIfMissing.
func (r *BookFileRepo) AddIfMissing(ctx context.Context, bookID int64, format, path string) (bool, error) {
	res, err := r.Track(ctx, bookID, format, path)
	if err != nil {
		return false, err
	}
	return res.Created(), nil
}

// MoveToBook makes bookID the owner of path, whoever held it before, and
// returns the previous owner (0 when the path was untracked, bookID when it
// was already the book's). It is for the explicit "this file belongs to that
// book" action (Fix match, #1238), never for an ordinary import, which must
// not take a file from another book (#2937). Runs in one transaction.
func (r *BookFileRepo) MoveToBook(ctx context.Context, bookID int64, format, path string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("book_files move begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prev int64
	err = tx.QueryRowContext(ctx, `SELECT book_id FROM book_files WHERE path = ?`, path).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO book_files (book_id, format, path, size_bytes, created_at) VALUES (?, ?, ?, 0, ?)`,
			bookID, format, path, time.Now().UTC()); err != nil {
			return 0, fmt.Errorf("book_files move insert: %w", err)
		}
	case err != nil:
		return 0, fmt.Errorf("book_files move lookup: %w", err)
	case prev == bookID:
		return prev, nil
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE book_files SET book_id = ?, format = ? WHERE path = ?`, bookID, format, path); err != nil {
			return 0, fmt.Errorf("book_files move update: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("book_files move commit: %w", err)
	}
	return prev, nil
}

// UpdatePath changes the on-disk path of the book_files row with the given id.
// Used by the library reorganize action (#1181) after a tracked file is moved
// to the location the current naming template computes. The path column is
// globally UNIQUE, so a move onto a path another row already owns fails here
// rather than silently corrupting the index.
func (r *BookFileRepo) UpdatePath(ctx context.Context, id int64, newPath string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE book_files SET path = ? WHERE id = ?`, newPath, id)
	if err != nil {
		return fmt.Errorf("book_files update path: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("book_files update path rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("book_files update path: no row with id %d", id)
	}
	pathEpochFor(r.db).Add(1)
	return nil
}

// BookIDForFile returns the owning book_id for a book_files row id, or 0 when
// no such row exists. Used by the reorganize apply path (#1181) to resolve a
// file the client picked back to its book.
func (r *BookFileRepo) BookIDForFile(ctx context.Context, fileID int64) (int64, error) {
	var bookID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT book_id FROM book_files WHERE id = ?`, fileID).Scan(&bookID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("book_files book_id lookup: %w", err)
	}
	return bookID, nil
}

// ListByBook returns all book_files rows for the given book, ordered by id.
func (r *BookFileRepo) ListByBook(ctx context.Context, bookID int64) ([]models.BookFile, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, book_id, format, path, size_bytes, created_at
		 FROM book_files WHERE book_id = ? ORDER BY id`,
		bookID)
	if err != nil {
		return nil, fmt.Errorf("book_files list: %w", err)
	}
	defer rows.Close()

	var files []models.BookFile
	for rows.Next() {
		var f models.BookFile
		if err := rows.Scan(&f.ID, &f.BookID, &f.Format, &f.Path, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("book_files scan: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ListByBooks returns every book_files row for any of the given book IDs, in
// one query, grouped by book_id. Used by the manual-import scan
// (ManualImportHandler.Scan, #2480) to replace an N+1 ListByBook call per
// confident catalogue match with a single round trip. Duplicate IDs collapse
// naturally (IN ignores repeats); an empty bookIDs returns an empty map.
func (r *BookFileRepo) ListByBooks(ctx context.Context, bookIDs []int64) (map[int64][]models.BookFile, error) {
	result := make(map[int64][]models.BookFile, len(bookIDs))
	if len(bookIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(bookIDs))
	args := make([]any, len(bookIDs))
	for i, id := range bookIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT id, book_id, format, path, size_bytes, created_at
		FROM book_files WHERE book_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY id` // #nosec G202 -- placeholders are generated from fixed ? tokens; book IDs remain bound args
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("book_files list by books: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var f models.BookFile
		if err := rows.Scan(&f.ID, &f.BookID, &f.Format, &f.Path, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("book_files scan: %w", err)
		}
		result[f.BookID] = append(result[f.BookID], f)
	}
	return result, rows.Err()
}

// DeleteByBook removes all book_files rows for the given book.
func (r *BookFileRepo) DeleteByBook(ctx context.Context, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM book_files WHERE book_id = ?`, bookID)
	if err != nil {
		return fmt.Errorf("book_files delete by book: %w", err)
	}
	return nil
}

// DeleteByPath removes the book_files row matching the given path and returns
// the book_id of the deleted row (0 if no row matched).
func (r *BookFileRepo) DeleteByPath(ctx context.Context, path string) (int64, error) {
	var bookID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT book_id FROM book_files WHERE path = ?`, path).Scan(&bookID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("book_files lookup by path: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM book_files WHERE path = ?`, path); err != nil {
		return 0, fmt.Errorf("book_files delete by path: %w", err)
	}
	return bookID, nil
}

// PathOwnedByOtherBook reports whether the given on-disk path is present in
// book_files under a book other than excludeBookID. The path column is globally
// UNIQUE, so there is at most one owner. Pass excludeBookID=0 to treat ANY
// registered owner as "another book" (e.g. when the current book's rows have
// already been cascade-deleted). The delete and reassign-cleanup paths use this
// to avoid os.Remove-ing a file another book still owns (#1368).
func (r *BookFileRepo) PathOwnedByOtherBook(ctx context.Context, path string, excludeBookID int64) (bool, error) {
	var owner int64
	err := r.db.QueryRowContext(ctx, `SELECT book_id FROM book_files WHERE path = ? LIMIT 1`, path).Scan(&owner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("book_files owner lookup: %w", err)
	}
	return owner != excludeBookID, nil
}

// ListAllPaths returns every path currently registered in book_files.
// Used by ScanLibrary to build the set of already-tracked files.
func (r *BookFileRepo) ListAllPaths(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT path FROM book_files`)
	if err != nil {
		return nil, fmt.Errorf("book_files list all paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("book_files scan path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// RecentEbookPaths returns up to limit ebook paths, newest first. The Calibre
// Test connection walks them for one that exists on Bindery's side and asks
// the plugin to open it through the push remap, because the library root on
// its own is an exact prefix match that never exercises the remap's join
// (#2831).
func (r *BookFileRepo) RecentEbookPaths(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT path FROM book_files WHERE format = 'ebook' ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("book_files recent ebook paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("book_files scan recent ebook path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
