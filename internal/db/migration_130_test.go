package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// m130Tables are the tables migration 130 rebuilds or whose rows hang off
// authors(id) and would go with a cascading DROP TABLE authors.
var m130Tables = []string{
	"authors", "author_identifiers", "author_aliases", "author_monitored_series",
	"books", "requests", "unmatched_units",
}

func m130Counts(t *testing.T, database *sql.DB) map[string]int {
	t.Helper()
	out := make(map[string]int, len(m130Tables))
	for _, table := range m130Tables {
		var n int
		if err := database.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// restorePre130Authors runs migration 130's Down section, which puts authors
// and author_identifiers back as 129 left them (foreign_id UNIQUE on both).
// It executes on the single pooled connection, so the bare PRAGMA lines take
// effect around the rebuild exactly as they do in a migration.
func restorePre130Authors(t *testing.T, database *sql.DB) {
	t.Helper()
	content, err := migrationsFS.ReadFile("migrations/130_authors_foreign_id_per_owner.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 130 has no Down section")
	}
	for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
		mustExec(t, database, stmt)
	}
	v130 := migrationVersionForTest(t, "130_authors_foreign_id_per_owner.sql")
	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`, v130)
}

func m130IndexExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// TestMigrate130KeepsEveryRowAcrossTheAuthorsRebuild upgrades a database at
// 129 holding authors, books and every kind of row that references an author,
// and checks the rebuild keeps all of them, leaves foreign_key_check clean,
// carries the AUTOINCREMENT high water mark and installs the new key.
func TestMigrate130KeepsEveryRowAcrossTheAuthorsRebuild(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	users := NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	aliases := NewAuthorAliasRepo(database)

	var seeded []*models.Author
	for i := 1; i <= 5; i++ {
		a := &models.Author{
			ForeignID: fmt.Sprintf("OL-M130-%d", i), Name: fmt.Sprintf("Author %d", i), SortName: fmt.Sprintf("%d, Author", i),
			Description: "bio", MetadataProvider: "openlibrary", Monitored: true,
		}
		owner := int64(0)
		if i%2 == 0 {
			owner = alice.ID
		}
		if err := authors.CreateForUser(ctx, a, owner); err != nil {
			t.Fatal(err)
		}
		if err := authors.UpsertAuthorIdentifier(ctx, a.ID, fmt.Sprintf("hc:m130-%d", i)); err != nil {
			t.Fatal(err)
		}
		if err := aliases.Create(ctx, &models.AuthorAlias{AuthorID: a.ID, Name: fmt.Sprintf("Pen Name %d", i)}); err != nil {
			t.Fatal(err)
		}
		for j := 1; j <= 2; j++ {
			b := &models.Book{
				ForeignID: fmt.Sprintf("OL-M130-%d-%d", i, j), AuthorID: a.ID, Title: fmt.Sprintf("Book %d %d", i, j),
				SortTitle: "b", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
				Monitored: true, OwnerUserID: owner,
			}
			if err := books.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		seeded = append(seeded, a)
	}
	mustExec(t, database, `INSERT INTO series (id, foreign_id, title) VALUES (77, 'S-M130', 'Saga')`)
	mustExec(t, database, `INSERT INTO author_monitored_series (author_id, series_id, created_at) VALUES (?, 77, CURRENT_TIMESTAMP)`, seeded[0].ID)
	mustExec(t, database, `INSERT INTO requests (owner_user_id, kind, foreign_id, status, result_author_id) VALUES (?, 'author', 'OL-M130-2', 'approved', ?)`, alice.ID, seeded[1].ID)
	mustExec(t, database, `INSERT INTO unmatched_units (unit_path, unit_kind, format, created_author_id, first_seen_at, last_seen_at, updated_at)
		VALUES ('/lib/x.epub', 'file', 'ebook', ?, 'now', 'now', 'now')`, seeded[2].ID)

	restorePre130Authors(t, database)
	if !m130IndexExists(t, database, "sqlite_autoindex_authors_1") {
		t.Fatal("restore did not bring back the 129 authors.foreign_id UNIQUE")
	}
	// Delete the newest author so the AUTOINCREMENT counter sits above the
	// highest surviving id; the rebuild must not hand that id out again.
	if err := authors.Delete(ctx, seeded[4].ID); err != nil {
		t.Fatal(err)
	}
	var seqBefore int64
	if err := database.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'authors'`).Scan(&seqBefore); err != nil {
		t.Fatal(err)
	}
	before := m130Counts(t, database)
	var snapshotBefore string
	if err := database.QueryRow(`SELECT group_concat(id || '|' || foreign_id || '|' || name || '|' || COALESCE(owner_user_id, 'null') || '|' || description || '|' || created_at, ';') FROM (SELECT * FROM authors ORDER BY id)`).Scan(&snapshotBefore); err != nil {
		t.Fatal(err)
	}

	if err := migrate(database); err != nil {
		t.Fatalf("migration 130: %v", err)
	}

	after := m130Counts(t, database)
	for _, table := range m130Tables {
		if before[table] != after[table] {
			t.Errorf("%s: %d rows before, %d after", table, before[table], after[table])
		}
		t.Logf("%s: %d rows before, %d after", table, before[table], after[table])
	}
	if before["authors"] != 4 || before["books"] != 8 || before["author_monitored_series"] != 1 {
		t.Fatalf("seed did not land as planned: %+v", before)
	}
	var snapshotAfter string
	if err := database.QueryRow(`SELECT group_concat(id || '|' || foreign_id || '|' || name || '|' || COALESCE(owner_user_id, 'null') || '|' || description || '|' || created_at, ';') FROM (SELECT * FROM authors ORDER BY id)`).Scan(&snapshotAfter); err != nil {
		t.Fatal(err)
	}
	if snapshotBefore != snapshotAfter {
		t.Errorf("author rows changed:\n before %s\n after  %s", snapshotBefore, snapshotAfter)
	}

	var violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Errorf("foreign_key_check reports %d violations after 130", violations)
	}

	var seqAfter int64
	if err := database.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'authors'`).Scan(&seqAfter); err != nil {
		t.Fatal(err)
	}
	if seqAfter != seqBefore {
		t.Errorf("AUTOINCREMENT counter = %d after 130, want %d", seqAfter, seqBefore)
	}
	fresh := &models.Author{ForeignID: "OL-M130-NEW", Name: "New", SortName: "New", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ID <= seqBefore {
		t.Errorf("new author got id %d, which a deleted author already used (counter %d)", fresh.ID, seqBefore)
	}

	for _, name := range []string{
		"idx_authors_owner_foreign_id", "idx_authors_foreign_id", "idx_authors_name", "idx_authors_owner",
		"idx_authors_sort_name", "idx_authors_sort_name_nocase", "idx_authors_sort_key", "idx_authors_name_sort_key",
		"idx_authors_quality_profile_id", "idx_author_identifiers_author_id", "idx_author_identifiers_provider",
		"idx_author_identifiers_foreign_id",
	} {
		if !m130IndexExists(t, database, name) {
			t.Errorf("index %s missing after 130", name)
		}
	}
	for _, gone := range []string{"sqlite_autoindex_authors_1", "sqlite_autoindex_author_identifiers_2"} {
		if m130IndexExists(t, database, gone) {
			t.Errorf("%s survived: the instance wide UNIQUE is still there", gone)
		}
	}
}

// After 130 a provider id is unique per owner, a NULL owner counting as one
// owner: two accounts may each hold a row, two shared rows may not.
func TestMigrate130UniquePerOwnerWithNullAsOneOwner(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "h")
	bob, _ := users.Create(ctx, "bob", "h")

	insert := func(owner any) error {
		_, err := database.ExecContext(ctx,
			`INSERT INTO authors (foreign_id, name, sort_name, owner_user_id) VALUES ('OL-U130', 'U', 'U', ?)`, owner)
		return err
	}
	if err := insert(alice.ID); err != nil {
		t.Fatalf("alice's row: %v", err)
	}
	if err := insert(bob.ID); err != nil {
		t.Fatalf("bob's row for the same author: %v", err)
	}
	if err := insert(alice.ID); err == nil {
		t.Error("a second row for alice was accepted")
	}
	if err := insert(nil); err != nil {
		t.Fatalf("first shared row: %v", err)
	}
	if err := insert(nil); err == nil {
		t.Error("a second shared row was accepted: NULL owners must count as one owner")
	}
}
