package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate134ImportListAudibleSyncOptions: existing lists get skip_owned on
// and remove_on_import off, the safe defaults, and both round-trip afterwards.
func TestMigrate134ImportListAudibleSyncOptions(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	mustExec(t, database, `DROP TABLE import_list_books`)
	mustExec(t, database, `ALTER TABLE import_lists DROP COLUMN remove_on_import`)
	mustExec(t, database, `ALTER TABLE import_lists DROP COLUMN skip_owned`)
	mustExec(t, database, `INSERT INTO import_lists (name, type, url, api_key, monitor_new, auto_add, enabled, media_type, created_at, updated_at)
		VALUES ('legacy', 'hardcover', 'want-to-read', '', 1, 1, 1, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)

	v134 := migrationVersionForTest(t, "134_import_list_audiblesync_options.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v134); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 134: %v", err)
	}

	repo := NewImportListRepo(database)
	lists, err := repo.List(ctx)
	if err != nil || len(lists) != 1 {
		t.Fatalf("lists = %v, %v", lists, err)
	}
	if !lists[0].SkipOwned || lists[0].RemoveOnImport {
		t.Fatalf("legacy row: SkipOwned=%v RemoveOnImport=%v, want true and false", lists[0].SkipOwned, lists[0].RemoveOnImport)
	}

	il := &models.ImportList{Name: "wishlist", Type: models.ImportListTypeAudibleSync, Enabled: true, SkipOwned: false, RemoveOnImport: true}
	if err := repo.Create(ctx, il); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, il.ID)
	if err != nil || got == nil || got.SkipOwned || !got.RemoveOnImport {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	got.SkipOwned, got.RemoveOnImport = true, false
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := repo.GetByID(ctx, il.ID); !again.SkipOwned || again.RemoveOnImport {
		t.Fatalf("after update = %+v", again)
	}
}

// TestImportListBooksRecordAndCascade covers the provenance table 134 adds:
// recording is idempotent, forgetting drops one row, and deleting the list or
// the book takes its rows with it.
func TestImportListBooksRecordAndCascade(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	lists := NewImportListRepo(database)
	books := NewBookRepo(database)
	author := mkAuthor(t, NewAuthorRepo(database), ctx, "OL-ILB-A")
	b1 := mkBook(t, books, ctx, author.ID, "OL-ILB-1", "One", models.BookStatusWanted)
	b2 := mkBook(t, books, ctx, author.ID, "OL-ILB-2", "Two", models.BookStatusWanted)
	il := &models.ImportList{Name: "u", Type: models.ImportListTypeAudibleSync, URL: models.ImportListSourceUnavailable, Enabled: true}
	if err := lists.Create(ctx, il); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{b1.ID, b1.ID, b2.ID} {
		if err := lists.RecordBook(ctx, il.ID, id, "B0", 40+id); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := lists.ListBooks(ctx, il.ID)
	if len(got) != 2 || got[0].SourceID != 40+b1.ID || got[1].SourceID != 40+b2.ID {
		t.Fatalf("recorded = %+v, want 2 with their source ids", got)
	}
	if err := lists.ForgetBook(ctx, il.ID, b1.ID); err != nil {
		t.Fatal(err)
	}
	if err := books.Delete(ctx, b2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := lists.ListBooks(ctx, il.ID); len(got) != 0 {
		t.Fatalf("after forget and book delete = %v, want none", got)
	}
	_ = lists.RecordBook(ctx, il.ID, b1.ID, "B0", 0)
	if err := lists.Delete(ctx, il.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM import_list_books`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after list delete = %d, %v", n, err)
	}
}

// TestMigrate134ReclassifiesAudibleIDs: rows written before "audible:" and
// "audnex:" ids named their own providers say "openlibrary"; the migration
// corrects them and leaves real OpenLibrary rows alone.
func TestMigrate134ReclassifiesAudibleIDs(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	audibleAuthor := mkAuthor(t, authors, ctx, "audible:AUTH1")
	olAuthor := mkAuthor(t, authors, ctx, "OL1A")
	mkBook(t, books, ctx, audibleAuthor.ID, "audible:B000000001", "Audible One", models.BookStatusWanted)
	mkBook(t, books, ctx, audibleAuthor.ID, "audnex:B000000002", "Audnex Two", models.BookStatusWanted)
	mkBook(t, books, ctx, olAuthor.ID, "OL1W", "Paper", models.BookStatusWanted)

	// As an older Bindery wrote them.
	for _, q := range []string{
		`UPDATE book_identifiers SET provider = 'openlibrary'`,
		`UPDATE author_identifiers SET provider = 'openlibrary'`,
		`UPDATE books SET metadata_provider = 'openlibrary'`,
		`UPDATE authors SET metadata_provider = 'openlibrary'`,
		`DROP TABLE import_list_books`,
		`ALTER TABLE import_lists DROP COLUMN remove_on_import`,
		`ALTER TABLE import_lists DROP COLUMN skip_owned`,
	} {
		mustExec(t, database, q)
	}
	v134 := migrationVersionForTest(t, "134_import_list_audiblesync_options.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v134); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 134: %v", err)
	}

	providers := func(q string) map[string]string {
		t.Helper()
		rows, err := database.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, p string
			if err := rows.Scan(&id, &p); err != nil {
				t.Fatal(err)
			}
			out[id] = p
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := map[string]string{"audible:B000000001": "audible", "audnex:B000000002": "audnex", "OL1W": "openlibrary"}
	for name, got := range map[string]map[string]string{
		"book_identifiers":        providers(`SELECT foreign_id, provider FROM book_identifiers`),
		"books.metadata_provider": providers(`SELECT foreign_id, metadata_provider FROM books`),
	} {
		for id, p := range want {
			if got[id] != p {
				t.Errorf("%s %s = %q, want %q", name, id, got[id], p)
			}
		}
	}
	wantAuthors := map[string]string{"audible:AUTH1": "audible", "OL1A": "openlibrary"}
	for name, got := range map[string]map[string]string{
		"author_identifiers":        providers(`SELECT foreign_id, provider FROM author_identifiers`),
		"authors.metadata_provider": providers(`SELECT foreign_id, metadata_provider FROM authors`),
	} {
		for id, p := range wantAuthors {
			if got[id] != p {
				t.Errorf("%s %s = %q, want %q", name, id, got[id], p)
			}
		}
	}
}
