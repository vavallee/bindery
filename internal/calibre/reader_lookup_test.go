package calibre

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedCalibreDB builds a tiny metadata.db with just enough schema to exercise
// LookupByTitleAuthor. We only need the three tables the query touches:
// books, authors, books_authors_link. The real Calibre schema has dozens
// more columns — none are referenced by our lookup so leaving them out is
// safe and keeps the fixture readable.
func seedCalibreDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := `
CREATE TABLE books (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    sort  TEXT
);
CREATE TABLE authors (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    sort TEXT
);
CREATE TABLE books_authors_link (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    book   INTEGER NOT NULL,
    author INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return path
}

func addCalibreBook(t *testing.T, dbPath, title, author string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`INSERT INTO books (title) VALUES (?)`, title)
	if err != nil {
		t.Fatal(err)
	}
	bookID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO authors (name) VALUES (?)`, author)
	if err != nil {
		t.Fatal(err)
	}
	authorID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO books_authors_link (book, author) VALUES (?, ?)`, bookID, authorID); err != nil {
		t.Fatal(err)
	}
	return bookID
}

func TestLookupByTitleAuthor_Found(t *testing.T) {
	dir := t.TempDir()
	seedCalibreDB(t, dir)
	want := addCalibreBook(t, filepath.Join(dir, "metadata.db"), "The Road", "Cormac McCarthy")

	id, found, err := LookupByTitleAuthor(context.Background(), dir, "The Road", "Cormac McCarthy")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("found should be true")
	}
	if id != want {
		t.Errorf("id = %d, want %d", id, want)
	}
}

func TestLookupByTitleAuthor_NotFound(t *testing.T) {
	dir := t.TempDir()
	seedCalibreDB(t, dir)
	addCalibreBook(t, filepath.Join(dir, "metadata.db"), "The Road", "Cormac McCarthy")

	_, found, err := LookupByTitleAuthor(context.Background(), dir, "No Country for Old Men", "Cormac McCarthy")
	if err != nil {
		t.Fatalf("not-found should return nil err, got %v", err)
	}
	if found {
		t.Error("found should be false for missing title")
	}
}

// TestLookupByTitleAuthor_MissingDB covers the "library_path points at a
// directory without metadata.db" case. We surface sql.ErrNoRows-equivalent
// silence (found=false, err!=nil) so the poller can log and retry rather
// than treating a missing library as a hard fail.
func TestLookupByTitleAuthor_MissingDB(t *testing.T) {
	dir := t.TempDir()
	_, found, err := LookupByTitleAuthor(context.Background(), dir, "x", "y")
	if err == nil {
		t.Error("missing metadata.db should return an error")
	}
	if found {
		t.Error("found should be false when DB is missing")
	}
}

func TestLookupByTitleAuthor_EmptyPath(t *testing.T) {
	_, found, err := LookupByTitleAuthor(context.Background(), "", "x", "y")
	if err == nil {
		t.Error("empty library path should be rejected")
	}
	if found {
		t.Error("found should be false for empty path")
	}
}

// TestLookupByTitleAuthor_MultipleAuthorsOnBook: Calibre allows a book to
// have multiple author rows via books_authors_link. A match on any one of
// them is enough — the drop-folder flow only passes the primary author and
// we don't want an "also wrote with" secondary author to hide the book.
func TestLookupByTitleAuthor_MultipleAuthorsOnBook(t *testing.T) {
	dir := t.TempDir()
	dbPath := seedCalibreDB(t, dir)
	bookID := addCalibreBook(t, dbPath, "Good Omens", "Terry Pratchett")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, _ := db.Exec(`INSERT INTO authors (name) VALUES (?)`, "Neil Gaiman")
	authorID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO books_authors_link (book, author) VALUES (?, ?)`, bookID, authorID); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"Terry Pratchett", "Neil Gaiman"} {
		id, found, err := LookupByTitleAuthor(context.Background(), dir, "Good Omens", name)
		if err != nil || !found || id != bookID {
			t.Errorf("author %q: id=%d found=%v err=%v", name, id, found, err)
		}
	}
}
