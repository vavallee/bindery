package calibre

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// reader_lookup.go is a SCAFFOLD kept small on purpose. Stream A owns
// internal/calibre/reader.go (a richer wrapper around Calibre's metadata.db);
// this file exists only so Path B can compile and ship an end-to-end
// drop-folder flow before Stream A merges. On rebase we refactor
// LookupByTitleAuthor to call Stream A's reader and delete this file.
//
// Keeping the surface here deliberately tiny (one function) minimises the
// blast radius of that refactor and makes the "did I forget something on
// rebase?" check trivial: only drop_folder.go consumes this.

// ErrCalibreDBNotFound is returned when the metadata.db file cannot be
// stat'd at the configured library path. The drop-folder flow translates
// this into a single warning + skip rather than blocking the Bindery
// import, since the operator may simply have mis-pointed library_path.
var ErrCalibreDBNotFound = errors.New("calibre metadata.db not found at library_path")

// LookupByTitleAuthor opens Calibre's metadata.db (read-only) at the given
// library path and returns the book id of the first row matching BOTH
// title and author name exactly. If no matching row is found, found=false
// with a nil error — the caller (drop-folder poller) uses that to decide
// whether to retry or give up.
//
// Exact match is deliberate: Calibre preserves titles verbatim from the
// metadata Bindery writes out, so the title we compare against is the one
// we just asked Calibre to ingest. A fuzzy match here would mask real
// "Calibre didn't pick up the file" problems behind spurious hits.
func LookupByTitleAuthor(ctx context.Context, libraryPath, title, author string) (int64, bool, error) {
	if libraryPath == "" {
		return 0, false, ErrCalibreDBNotFound
	}
	dbPath := filepath.Join(libraryPath, "metadata.db")
	// SQLite DSN: mode=ro so a concurrent Calibre writer can't be blocked
	// by us, and immutable=0 (the default) so we see new rows as Calibre
	// ingests them — crucial for the poll loop.
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, false, fmt.Errorf("open calibre metadata.db: %w", err)
	}
	defer db.Close()

	// Calibre's schema: books + authors + books_authors_link. A single
	// book may have several authors (primary + secondary); matching any
	// one of them is enough to claim the row — the drop-folder writer
	// only provides the primary author name anyway.
	const q = `
SELECT b.id
FROM books b
JOIN books_authors_link bal ON bal.book = b.id
JOIN authors a ON a.id = bal.author
WHERE b.title = ? AND a.name = ?
ORDER BY b.id DESC
LIMIT 1`

	var id int64
	err = db.QueryRowContext(ctx, q, title, author).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query calibre books: %w", err)
	}
	return id, true, nil
}
