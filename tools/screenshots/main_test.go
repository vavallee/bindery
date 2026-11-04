package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

// The seed has to keep working as the schema moves, or `make screenshots`
// only finds out the next time someone wants new pictures.
func TestSeedFillsEveryPhotographedPage(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	now := time.Date(2026, time.February, 28, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := seed(ctx, database, now); err != nil {
		t.Fatal(err)
	}

	authors, err := db.NewAuthorRepo(database).List(ctx)
	if err != nil || len(authors) != 6 {
		t.Fatalf("authors = %d, err %v", len(authors), err)
	}
	books, err := db.NewBookRepo(database).List(ctx)
	if err != nil || len(books) != 22 {
		t.Fatalf("books = %d, err %v", len(books), err)
	}
	thisMonth := 0
	for _, b := range books {
		if b.ReleaseDate != nil && b.ReleaseDate.Year() == 2026 && b.ReleaseDate.Month() == time.February {
			thisMonth++
		}
	}
	if thisMonth != 4 {
		t.Errorf("calendar entries this month = %d, want 4", thisMonth)
	}
	downloads, err := db.NewDownloadRepo(database).List(ctx)
	if err != nil || len(downloads) != 5 {
		t.Fatalf("downloads = %d, err %v", len(downloads), err)
	}
}

func TestRunSeedsOneFreshDatabase(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("no database path: want a usage error")
	}
	path := filepath.Join(t.TempDir(), "bindery.db")
	if err := run([]string{path}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path}); err == nil {
		t.Error("second seed of the same database: want an error, not duplicate rows")
	}
}
