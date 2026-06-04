package db

import (
	"context"
	"testing"
)

func TestMigration051AppliesAndIndexExists(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	var name string
	err = d.QueryRowContext(ctx, "SELECT name FROM pragma_table_info('books') WHERE name='dedup_key'").Scan(&name)
	if err != nil || name != "dedup_key" {
		t.Fatalf("dedup_key column missing: %v (%q)", err, name)
	}
	var idx string
	err = d.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_books_author_dedup_key'").Scan(&idx)
	if err != nil || idx != "idx_books_author_dedup_key" {
		t.Fatalf("index missing: %v (%q)", err, idx)
	}
	var ver int
	if err := d.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE version=51").Scan(&ver); err != nil {
		t.Fatalf("migration 51 not recorded: %v", err)
	}
}
