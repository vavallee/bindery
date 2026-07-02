package db

import (
	"context"
	"testing"
)

func TestGrimmoryPushRepo(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := NewGrimmoryPushRepo(database)
	ctx := context.Background()

	has, err := repo.Has(ctx, "/books/a.epub")
	if err != nil || has {
		t.Fatalf("Has on empty table = %v, %v; want false, nil", has, err)
	}
	last, count, err := repo.LastPush(ctx)
	if err != nil || count != 0 || !last.IsZero() {
		t.Fatalf("LastPush on empty table = %v, %d, %v; want zero, 0, nil", last, count, err)
	}

	if err := repo.Record(ctx, 7, "/books/a.epub", 42); err != nil {
		t.Fatal(err)
	}
	// Cleaned-path lookups match, and re-recording is a silent no-op.
	has, err = repo.Has(ctx, "/books//a.epub")
	if err != nil || !has {
		t.Fatalf("Has after record = %v, %v; want true, nil", has, err)
	}
	if err := repo.Record(ctx, 8, "/books/a.epub", 99); err != nil {
		t.Fatalf("re-record should be a no-op, got %v", err)
	}

	if err := repo.Record(ctx, 9, "/books/b.epub", 0); err != nil {
		t.Fatal(err)
	}
	last, count, err = repo.LastPush(ctx)
	if err != nil || count != 2 || last.IsZero() {
		t.Fatalf("LastPush = %v, %d, %v; want non-zero time and count 2", last, count, err)
	}
}
