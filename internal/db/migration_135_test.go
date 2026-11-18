package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate135UpAndDown runs migration 135's Down on a fresh database, checks
// the column is gone and an edition row survives it, then runs Up again and
// checks the existing row reads as nothing locked (#3216).
func TestMigrate135UpAndDown(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-135-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-135-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book", Status: models.BookStatusWanted}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	editions := NewEditionRepo(database)
	if err := editions.Upsert(ctx, &models.Edition{ForeignID: "ed-135", BookID: b.ID, Title: "Book"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, `UPDATE editions SET locked_fields = '["publisher"]' WHERE foreign_id = 'ed-135'`)

	content, err := migrationsFS.ReadFile("migrations/135_edition_locked_fields.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 135 has no Down section")
	}
	for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
		mustExec(t, database, stmt)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('editions') WHERE name = 'locked_fields'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("Down left editions.locked_fields in place")
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM editions WHERE foreign_id = 'ed-135'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("edition row lost by Down: n=%d err=%v", n, err)
	}

	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`,
		migrationVersionForTest(t, "135_edition_locked_fields.sql"))
	if err := migrate(database); err != nil {
		t.Fatalf("migrate 135 again: %v", err)
	}
	got, err := editions.GetByForeignID(ctx, "ed-135")
	if err != nil || got == nil {
		t.Fatalf("reload edition: %v", err)
	}
	if got.LockedFields == nil || len(got.LockedFields) != 0 {
		t.Fatalf("existing edition after Up: lockedFields = %#v, want empty", got.LockedFields)
	}
}
