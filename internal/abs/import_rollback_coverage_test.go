package abs

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// covImpEnv bundles an importer with the raw database so a test can install
// SQLite triggers that make one specific write fail. The repos are concrete
// types, so a trigger is the narrowest way to reach a single error branch.
type covImpEnv struct {
	db         *sql.DB
	importer   *Importer
	authors    *db.AuthorRepo
	books      *db.BookRepo
	series     *db.SeriesRepo
	editions   *db.EditionRepo
	provenance *db.ABSProvenanceRepo
	runs       *db.ABSImportRunRepo
	entities   *db.ABSImportRunEntityRepo
	reviews    *db.ABSReviewItemRepo
	conflicts  *db.ABSMetadataConflictRepo
	settings   *db.SettingsRepo
	aliases    *db.AuthorAliasRepo
}

func covImpNewEnv(t *testing.T) *covImpEnv {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	env := &covImpEnv{
		db:         database,
		authors:    db.NewAuthorRepo(database),
		books:      db.NewBookRepo(database),
		series:     db.NewSeriesRepo(database),
		editions:   db.NewEditionRepo(database),
		provenance: db.NewABSProvenanceRepo(database),
		runs:       db.NewABSImportRunRepo(database),
		entities:   db.NewABSImportRunEntityRepo(database),
		reviews:    db.NewABSReviewItemRepo(database),
		conflicts:  db.NewABSMetadataConflictRepo(database),
		settings:   db.NewSettingsRepo(database),
		aliases:    db.NewAuthorAliasRepo(database),
	}
	env.importer = NewImporter(env.authors, env.aliases, env.books, env.editions, env.series, env.settings,
		env.runs, env.entities, env.provenance, env.reviews, env.conflicts)
	return env
}

func (e *covImpEnv) exec(t *testing.T, stmt string, args ...any) {
	t.Helper()
	if _, err := e.db.ExecContext(context.Background(), stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// failOn installs a trigger that aborts every <op> on table (optionally only
// rows matching when, written against OLD/NEW) with the message covimp boom.
func (e *covImpEnv) failOn(t *testing.T, name, op, table, when string) {
	t.Helper()
	timing := "BEFORE " + op + " ON " + table
	if when != "" {
		timing += " WHEN " + when
	}
	e.exec(t, "CREATE TRIGGER "+name+" "+timing+" BEGIN SELECT RAISE(ABORT, 'covimp boom'); END")
}

func (e *covImpEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func covImpActionsWith(result *RollbackResult, action, reasonSubstr string) int {
	n := 0
	for _, a := range result.Actions {
		if a.Action == action && strings.Contains(a.Reason, reasonSubstr) {
			n++
		}
	}
	return n
}

func covImpRequireFailedRollback(t *testing.T, env *covImpEnv, runID int64, result *RollbackResult) {
	t.Helper()
	if result.Stats.Failed == 0 {
		t.Fatalf("rollback stats = %+v, want failures", result.Stats)
	}
	if covImpActionsWith(result, "skip", "covimp boom") == 0 {
		t.Fatalf("actions = %+v, want a skip carrying the trigger error", result.Actions)
	}
	if result.Status == runStatusRolledBack {
		t.Fatalf("status = %q, a failed rollback must not mark the run rolled back", result.Status)
	}
	run, err := env.runs.GetByID(context.Background(), runID)
	if err != nil || run == nil {
		t.Fatalf("GetByID: %v %v", run, err)
	}
	if run.Status == runStatusRolledBack {
		t.Fatalf("persisted status = %q, want unchanged after failed rollback", run.Status)
	}
}

func TestCovImpRollback_FreshImportWriteFailuresLeaveRunIntact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		op    string
		table string
		when  string
	}{
		{name: "book_delete", op: "DELETE", table: "books"},
		{name: "edition_delete", op: "DELETE", table: "editions"},
		{name: "author_delete", op: "DELETE", table: "authors"},
		{name: "series_delete", op: "DELETE", table: "series"},
		{name: "series_unlink", op: "DELETE", table: "series_books"},
		{name: "provenance_delete_all", op: "DELETE", table: "abs_provenance"},
		{name: "provenance_delete_edition", op: "DELETE", table: "abs_provenance", when: "OLD.entity_type = 'edition'"},
		{name: "provenance_delete_book", op: "DELETE", table: "abs_provenance", when: "OLD.entity_type = 'book'"},
		{name: "provenance_delete_series", op: "DELETE", table: "abs_provenance", when: "OLD.entity_type = 'series'"},
		{name: "provenance_delete_author", op: "DELETE", table: "abs_provenance", when: "OLD.entity_type = 'author'"},
		{name: "run_status", op: "UPDATE", table: "abs_import_runs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			ctx := context.Background()
			runID := runSingleABSImport(t, env.importer, sampleABSItem())
			booksBefore := env.count(t, "SELECT COUNT(*) FROM books")
			if booksBefore != 1 {
				t.Fatalf("books after import = %d, want 1", booksBefore)
			}
			env.failOn(t, "covimp_"+tc.name, tc.op, tc.table, tc.when)

			result, err := env.importer.Rollback(ctx, runID)
			if tc.table == "abs_import_runs" {
				// Every entity rolls back cleanly; only the final status write fails.
				if err == nil || !strings.Contains(err.Error(), "covimp boom") {
					t.Fatalf("Rollback err = %v, want status update failure", err)
				}
				if n := env.count(t, "SELECT COUNT(*) FROM books"); n != 0 {
					t.Fatalf("books = %d, want entity rollback to have completed", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			covImpRequireFailedRollback(t, env, runID, result)
		})
	}
}

func TestCovImpRollback_NotOwnedByRunSkipsDestructiveActions(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	// The provenance rows survive but no longer name this run as owner, so
	// every created-entity case must refuse to delete anything.
	env.exec(t, "UPDATE abs_provenance SET import_run_id = NULL")

	result, err := env.importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.Stats.EntitiesDeleted != 0 || result.Stats.ProvenanceUnlinked != 0 {
		t.Fatalf("stats = %+v, want nothing deleted or unlinked", result.Stats)
	}
	for _, kind := range []string{"book", "author", "edition", "series"} {
		if covImpActionsWith(result, "skip", "no longer the current provenance owner for this "+kind) == 0 {
			t.Fatalf("actions = %+v, want ownership skip for %s", result.Actions, kind)
		}
	}
	if n := env.count(t, "SELECT COUNT(*) FROM books"); n != 1 {
		t.Fatalf("books = %d, want the unowned book kept", n)
	}
	if n := env.count(t, "SELECT COUNT(*) FROM authors"); n != 1 {
		t.Fatalf("authors = %d, want the unowned author kept", n)
	}
	if result.Status != runStatusRolledBack {
		t.Fatalf("status = %q, skips alone are not failures", result.Status)
	}
}

func TestCovImpRollback_ProvenancePointsElsewhereIsSkipped(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	env.exec(t, "UPDATE abs_provenance SET local_id = local_id + 1000")

	result, err := env.importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := covImpActionsWith(result, "skip", "different local entity"); got != len(result.Actions) || got == 0 {
		t.Fatalf("actions = %+v, want every action skipped as relinked", result.Actions)
	}
	if result.Stats.Skipped != len(result.Actions) {
		t.Fatalf("stats = %+v, want all skipped", result.Stats)
	}
	if n := env.count(t, "SELECT COUNT(*) FROM books"); n != 1 {
		t.Fatalf("books = %d, want kept", n)
	}
}

func TestCovImpRollback_ProvenanceLookupErrorMarksInspect(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	env.exec(t, "ALTER TABLE abs_provenance RENAME TO covimp_abs_provenance_gone")

	result, err := env.importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(result.Actions) == 0 || result.Stats.Failed != len(result.Actions) {
		t.Fatalf("result = %+v, want every entity failed", result)
	}
	for _, a := range result.Actions {
		if a.Action != "inspect" || a.Reason == "" {
			t.Fatalf("action = %+v, want inspect with the lookup error", a)
		}
	}
	// Display names still resolve from the catalog even when provenance is gone.
	foundTitle := false
	for _, a := range result.Actions {
		if a.EntityType == entityTypeBook && a.DisplayName == "Project Hail Mary" {
			foundTitle = true
		}
	}
	if !foundTitle {
		t.Fatalf("actions = %+v, want the book display name", result.Actions)
	}
}

func TestCovImpRollback_RetainedByOtherLinksUnlinksOnly(t *testing.T) {
	t.Parallel()
	for _, preview := range []bool{true, false} {
		preview := preview
		name := "apply"
		if preview {
			name = "preview"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			ctx := context.Background()
			runID := runSingleABSImport(t, env.importer, sampleABSItem())
			// Mirror every local entity under another library with no run owner,
			// as if a second import (or a manual link) still references them.
			env.exec(t, `INSERT INTO abs_provenance (source_id, library_id, entity_type, external_id, local_id, item_id)
				SELECT source_id, 'lib-other', entity_type, external_id, local_id, item_id FROM abs_provenance`)

			var result *RollbackResult
			var err error
			if preview {
				result, err = env.importer.RollbackPreview(ctx, runID)
			} else {
				result, err = env.importer.Rollback(ctx, runID)
			}
			if err != nil {
				t.Fatalf("rollback: %v", err)
			}
			// Editions have no cross-link check and still go; books and
			// authors referenced elsewhere must only be unlinked.
			for _, a := range result.Actions {
				if a.Action == "delete_book" || a.Action == "delete_author" {
					t.Fatalf("action = %+v, want no book/author deletion while other links exist", a)
				}
			}
			for _, reason := range []string{
				"local book retained",
				"local author retained",
			} {
				if covImpActionsWith(result, "unlink_provenance", reason) == 0 {
					t.Fatalf("actions = %+v, want %q", result.Actions, reason)
				}
			}
			if n := env.count(t, "SELECT COUNT(*) FROM books"); n != 1 {
				t.Fatalf("books = %d, want retained", n)
			}
			if n := env.count(t, "SELECT COUNT(*) FROM authors"); n != 1 {
				t.Fatalf("authors = %d, want retained", n)
			}
			own := env.count(t, "SELECT COUNT(*) FROM abs_provenance WHERE library_id = ?", "lib-books")
			if preview && own == 0 {
				t.Fatal("preview removed provenance rows")
			}
			if !preview {
				if own != 0 {
					t.Fatalf("own provenance rows = %d, want unlinked", own)
				}
				if result.Stats.ProvenanceUnlinked == 0 {
					t.Fatalf("stats = %+v, want unlinks", result.Stats)
				}
			}
			if other := env.count(t, "SELECT COUNT(*) FROM abs_provenance WHERE library_id = 'lib-other'"); other == 0 {
				t.Fatal("other library's provenance must survive")
			}
		})
	}
}

func TestCovImpRollback_RetainedUnlinkFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"book", "author", "series"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			ctx := context.Background()
			runID := runSingleABSImport(t, env.importer, sampleABSItem())
			env.exec(t, `INSERT INTO abs_provenance (source_id, library_id, entity_type, external_id, local_id, item_id)
				SELECT source_id, 'lib-other', entity_type, external_id, local_id, item_id FROM abs_provenance`)
			env.failOn(t, "covimp_prov_"+kind, "DELETE", "abs_provenance", "OLD.entity_type = '"+kind+"'")
			result, err := env.importer.Rollback(ctx, runID)
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			covImpRequireFailedRollback(t, env, runID, result)
		})
	}
}

func TestCovImpRollback_AuthorKeepsUnrelatedBooks(t *testing.T) {
	t.Parallel()
	for _, failUnlink := range []bool{false, true} {
		failUnlink := failUnlink
		t.Run(map[bool]string{false: "unlink", true: "unlink_fails"}[failUnlink], func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			ctx := context.Background()
			runID := runSingleABSImport(t, env.importer, sampleABSItem())
			var authorID int64
			if err := env.db.QueryRowContext(ctx, "SELECT id FROM authors").Scan(&authorID); err != nil {
				t.Fatalf("author id: %v", err)
			}
			manual := &models.Book{ForeignID: "covimp-manual", AuthorID: authorID, Title: "Manual Book", Status: models.BookStatusWanted}
			if err := env.books.Create(ctx, manual); err != nil {
				t.Fatalf("Create manual book: %v", err)
			}
			if failUnlink {
				env.failOn(t, "covimp_prov_author", "DELETE", "abs_provenance", "OLD.entity_type = 'author'")
			}
			result, err := env.importer.Rollback(ctx, runID)
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if failUnlink {
				covImpRequireFailedRollback(t, env, runID, result)
				return
			}
			if covImpActionsWith(result, "unlink_provenance", "still has linked books") != 1 {
				t.Fatalf("actions = %+v, want author retained for the manual book", result.Actions)
			}
			author, err := env.authors.GetByID(ctx, authorID)
			if err != nil || author == nil {
				t.Fatalf("author after rollback = %v err=%v, want kept", author, err)
			}
			if b, _ := env.books.GetByID(ctx, manual.ID); b == nil {
				t.Fatal("manual book was deleted by rollback")
			}
		})
	}
}

func TestCovImpRollback_AuthorDeletesRunBooksNotYetRemoved(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	// The book case fails to delete the book; the author case then sees a
	// run-owned book still attached, retries the delete and must block.
	entities, err := env.entities.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	var authorID int64
	for _, e := range entities {
		if e.EntityType == entityTypeAuthor {
			authorID = e.LocalID
		}
	}
	if authorID == 0 {
		t.Fatalf("entities = %+v, want author", entities)
	}
	env.failOn(t, "covimp_book_once", "DELETE", "books", "")

	result, err := env.importer.Rollback(ctx, runID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	covImpRequireFailedRollback(t, env, runID, result)
	// The author pass retried the run-owned book delete and was blocked.
	blocked := false
	for _, a := range result.Actions {
		if a.EntityType == entityTypeAuthor && a.Action == "skip" && strings.Contains(a.Reason, "covimp boom") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("actions = %+v, want the author blocked by the book delete failure", result.Actions)
	}
	if a, _ := env.authors.GetByID(ctx, authorID); a == nil {
		t.Fatal("author deleted despite blocked book delete")
	}
}

func covImpSeedExistingBook(t *testing.T, env *covImpEnv, item NormalizedLibraryItem) *models.Book {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-COVIMP-AUTHOR", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true}
	if err := env.authors.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	book := &models.Book{
		ForeignID:   "OL-COVIMP-BOOK",
		AuthorID:    author.ID,
		Title:       "Local Title",
		SortTitle:   "Local Title",
		Description: "Local description.",
		Language:    "fre",
		MediaType:   models.MediaTypeEbook,
		Status:      models.BookStatusSkipped,
	}
	if err := env.books.Create(ctx, book); err != nil {
		t.Fatalf("Create book: %v", err)
	}
	if err := env.provenance.Upsert(ctx, &models.ABSProvenance{
		SourceID: DefaultSourceID, LibraryID: item.LibraryID, EntityType: entityTypeBook,
		ExternalID: item.ItemID, LocalID: book.ID, ItemID: item.ItemID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	return book
}

func TestCovImpRollback_RestoreBookFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(t *testing.T, env *covImpEnv, book *models.Book)
		reason string
		failed bool
	}{
		{
			name: "update_fails",
			setup: func(t *testing.T, env *covImpEnv, _ *models.Book) {
				env.failOn(t, "covimp_book_update", "UPDATE", "books", "")
			},
			reason: "covimp boom",
			failed: true,
		},
		{
			name: "unlink_fails",
			setup: func(t *testing.T, env *covImpEnv, _ *models.Book) {
				env.failOn(t, "covimp_book_prov", "DELETE", "abs_provenance", "OLD.entity_type = 'book'")
			},
			reason: "covimp boom",
			failed: true,
		},
		{
			name: "book_gone",
			setup: func(t *testing.T, env *covImpEnv, book *models.Book) {
				env.exec(t, "PRAGMA foreign_keys = OFF")
				env.exec(t, "DELETE FROM books WHERE id = ?", book.ID)
				env.exec(t, "PRAGMA foreign_keys = ON")
			},
			reason: "book no longer exists",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			ctx := context.Background()
			item := sampleABSItem()
			item.Title = "ABS Title"
			book := covImpSeedExistingBook(t, env, item)
			runID := runSingleABSImport(t, env.importer, item)
			if got, _ := env.books.GetByID(ctx, book.ID); got == nil || got.Title != "ABS Title" {
				t.Fatalf("book after import = %+v, want ABS title", got)
			}
			tc.setup(t, env, book)
			result, err := env.importer.Rollback(ctx, runID)
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if covImpActionsWith(result, "skip", tc.reason) == 0 {
				t.Fatalf("actions = %+v, want skip %q", result.Actions, tc.reason)
			}
			if tc.failed {
				covImpRequireFailedRollback(t, env, runID, result)
			}
		})
	}
}

func covImpSeedExistingAuthorRun(t *testing.T, env *covImpEnv) (int64, *models.Author) {
	t.Helper()
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        absForeignID("author", "lib-books", "author-andy-weir"),
		Name:             "A. Weir",
		SortName:         "Weir, A.",
		MetadataProvider: providerAudiobookshelf,
		Monitored:        true,
	}
	if err := env.authors.Create(ctx, existing); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	if err := env.provenance.Upsert(ctx, &models.ABSProvenance{
		SourceID: DefaultSourceID, LibraryID: "lib-books", EntityType: entityTypeAuthor,
		ExternalID: "author-andy-weir", LocalID: existing.ID, ItemID: "li-project-hail-mary",
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: "OL-ANDY", Name: "Andy Weir"}},
		authors: map[string]*models.Author{
			"OL-ANDY": {ForeignID: "OL-ANDY", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: "openlibrary"},
		},
	}
	env.importer.WithMetadata(metadata.NewAggregator(provider))
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	if got, _ := env.authors.GetByID(ctx, existing.ID); got == nil || got.Name != "Andy Weir" {
		t.Fatalf("author after import = %+v, want upstream name", got)
	}
	return runID, existing
}

func TestCovImpRollback_RestoreAuthorFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(t *testing.T, env *covImpEnv, author *models.Author)
		reason string
		failed bool
	}{
		{
			name: "update_fails",
			setup: func(t *testing.T, env *covImpEnv, _ *models.Author) {
				env.failOn(t, "covimp_author_update", "UPDATE", "authors", "")
			},
			reason: "covimp boom",
			failed: true,
		},
		{
			name: "identifier_delete_fails",
			setup: func(t *testing.T, env *covImpEnv, _ *models.Author) {
				env.failOn(t, "covimp_ident_delete", "DELETE", "author_identifiers", "")
			},
			reason: "covimp boom",
			failed: true,
		},
		{
			name: "unlink_fails",
			setup: func(t *testing.T, env *covImpEnv, _ *models.Author) {
				env.failOn(t, "covimp_author_prov", "DELETE", "abs_provenance", "OLD.entity_type = 'author'")
			},
			reason: "covimp boom",
			failed: true,
		},
		{
			name: "author_gone",
			setup: func(t *testing.T, env *covImpEnv, author *models.Author) {
				env.exec(t, "PRAGMA foreign_keys = OFF")
				env.exec(t, "DELETE FROM authors WHERE id = ?", author.ID)
				env.exec(t, "PRAGMA foreign_keys = ON")
			},
			reason: "author no longer exists",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := covImpNewEnv(t)
			runID, author := covImpSeedExistingAuthorRun(t, env)
			tc.setup(t, env, author)
			result, err := env.importer.Rollback(context.Background(), runID)
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if covImpActionsWith(result, "skip", tc.reason) == 0 {
				t.Fatalf("actions = %+v, want skip %q", result.Actions, tc.reason)
			}
			if tc.failed {
				covImpRequireFailedRollback(t, env, runID, result)
			}
		})
	}
}

func TestCovImpRollback_GuardsAndRunLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	bare := &Importer{}
	if runs, err := bare.RecentRuns(ctx, 5); runs != nil || err != nil {
		t.Fatalf("RecentRuns without repo = %v %v, want nil nil", runs, err)
	}
	if run, err := bare.GetRun(ctx, 1); run != nil || err != nil {
		t.Fatalf("GetRun without repo = %v %v, want nil nil", run, err)
	}
	if _, err := bare.Rollback(ctx, 1); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Rollback without repos err = %v, want unavailable", err)
	}

	env := covImpNewEnv(t)
	if _, err := env.importer.Rollback(ctx, 424242); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Rollback missing run err = %v, want not found", err)
	}

	dry := &models.ABSImportRun{SourceID: DefaultSourceID, LibraryID: "lib-books", Status: "completed", DryRun: true}
	if err := env.runs.Create(ctx, dry); err != nil {
		t.Fatalf("Create dry run: %v", err)
	}
	result, err := env.importer.Rollback(ctx, dry.ID)
	if err != nil {
		t.Fatalf("Rollback dry run: %v", err)
	}
	if !result.DryRun || len(result.Actions) != 0 || result.Status == runStatusRolledBack {
		t.Fatalf("dry-run rollback = %+v, want a no-op result", result)
	}

	env.exec(t, "ALTER TABLE abs_import_runs RENAME TO covimp_runs_gone")
	if _, err := env.importer.Rollback(ctx, dry.ID); err == nil {
		t.Fatal("Rollback with broken runs table returned nil error")
	}
	if _, err := env.importer.RecentRuns(ctx, 1); err == nil {
		t.Fatal("RecentRuns with broken runs table returned nil error")
	}
}

func TestCovImpRollback_ListByRunErrorIsReturned(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	env.exec(t, "ALTER TABLE abs_import_run_entities RENAME TO covimp_entities_gone")
	if _, err := env.importer.Rollback(ctx, runID); err == nil {
		t.Fatal("Rollback with broken entity table returned nil error")
	}
}

func TestCovImpHydrateRun_DecodesCheckpointAndIgnoresGarbage(t *testing.T) {
	t.Parallel()
	run := models.ABSImportRun{
		ID:               7,
		SourceID:         "src",
		SourceLabel:      "Shelf",
		BaseURL:          "https://abs.example.com",
		LibraryID:        "lib-1",
		Status:           "running",
		SourceConfigJSON: `{"label":"From Config"}`,
		CheckpointJSON:   `{"libraryId":"lib-1","page":3}`,
		SummaryJSON:      `{"dryRun":false}`,
	}
	out := HydrateRun(run)
	if out.Checkpoint == nil || out.Checkpoint.LibraryID != "lib-1" || out.Checkpoint.Page != 3 {
		t.Fatalf("checkpoint = %+v, want lib-1 page 3", out.Checkpoint)
	}
	if out.Source.Label != "From Config" {
		t.Fatalf("source label = %q, want config JSON to override", out.Source.Label)
	}

	run.CheckpointJSON = `{not json`
	if out := HydrateRun(run); out.Checkpoint != nil {
		t.Fatalf("checkpoint = %+v, want nil for malformed JSON", out.Checkpoint)
	}
	run.CheckpointJSON = "{}"
	if out := HydrateRun(run); out.Checkpoint != nil {
		t.Fatalf("checkpoint = %+v, want nil for empty object", out.Checkpoint)
	}
}

func TestCovImpRollbackHelpers(t *testing.T) {
	t.Parallel()
	// metadataBookID accepts every numeric shape JSON decoding can produce.
	for _, tc := range []struct {
		in   any
		want int64
	}{
		{float64(4), 4}, {int64(5), 5}, {6, 6}, {"7", 0}, {nil, 0},
	} {
		if got := metadataBookID(map[string]any{"bookId": tc.in}); got != tc.want {
			t.Fatalf("metadataBookID(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}

	// restore* only revert values that still equal the post-import snapshot.
	changed := false
	i64 := int64(2)
	restoreInt64(&i64, 1, 3, &changed)
	if i64 != 2 || changed {
		t.Fatalf("restoreInt64 touched an edited value: %d %v", i64, changed)
	}
	restoreInt64(&i64, 1, 2, &changed)
	if i64 != 1 || !changed {
		t.Fatalf("restoreInt64 = %d %v, want 1 true", i64, changed)
	}
	f := 2.5
	changed = false
	restoreFloat64(&f, 1.5, 9, &changed)
	restoreFloat64(&f, 1.5, 2.5, &changed)
	if f != 1.5 || !changed {
		t.Fatalf("restoreFloat64 = %v %v", f, changed)
	}
	b := true
	changed = false
	restoreBool(&b, false, false, &changed)
	restoreBool(&b, false, true, &changed)
	if b || !changed {
		t.Fatalf("restoreBool = %v %v", b, changed)
	}
	one, two := int64(1), int64(2)
	ptr := &two
	changed = false
	restoreInt64Ptr(&ptr, &one, nil, &changed)
	if ptr != &two || changed {
		t.Fatal("restoreInt64Ptr touched an edited pointer")
	}
	restoreInt64Ptr(&ptr, &one, &two, &changed)
	if ptr == nil || *ptr != 1 || !changed {
		t.Fatalf("restoreInt64Ptr = %v %v", ptr, changed)
	}
	if restoreBookFromSnapshot(nil, nil, nil) || restoreAuthorFromSnapshot(nil, nil, nil) {
		t.Fatal("nil snapshots must report no change")
	}
	if !equalInt64Ptr(nil, nil) || equalInt64Ptr(&one, nil) || equalTimePtr(nil, mustDate(t, "2020-01-01")) {
		t.Fatal("pointer equality helpers disagree on nil handling")
	}
	if equalStrings([]string{"a"}, []string{"b"}) || equalStrings([]string{"a"}, nil) {
		t.Fatal("equalStrings matched different slices")
	}
}

func TestCovImpRollbackActionDisplayName(t *testing.T) {
	t.Parallel()
	env := covImpNewEnv(t)
	ctx := context.Background()
	runID := runSingleABSImport(t, env.importer, sampleABSItem())
	entities, err := env.entities.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	names := map[string]string{}
	for _, e := range entities {
		names[e.EntityType] = env.importer.rollbackActionDisplayName(ctx, e)
	}
	if names[entityTypeBook] != "Project Hail Mary" || names[entityTypeAuthor] != "Andy Weir" || names[entityTypeSeries] != "Standalone" {
		t.Fatalf("display names = %+v", names)
	}
	if got := env.importer.rollbackActionDisplayName(ctx, models.ABSImportRunEntity{EntityType: "mystery", LocalID: 1}); got != "" {
		t.Fatalf("unknown type display name = %q, want empty", got)
	}
	if got := env.importer.rollbackActionDisplayName(ctx, models.ABSImportRunEntity{EntityType: entityTypeBook}); got != "" {
		t.Fatalf("zero local id display name = %q, want empty", got)
	}
	bare := &Importer{}
	for _, kind := range []string{entityTypeBook, entityTypeAuthor, entityTypeSeries, entityTypeEdition} {
		if got := bare.rollbackActionDisplayName(ctx, models.ABSImportRunEntity{EntityType: kind, LocalID: 1}); got != "" {
			t.Fatalf("%s display name without repo = %q, want empty", kind, got)
		}
	}
	if ok, err := bare.hasProvenanceOutsideRun(ctx, entityTypeBook, 1, runID); ok || err != nil {
		t.Fatalf("hasProvenanceOutsideRun without repo = %v %v", ok, err)
	}
	if ok, err := bare.hasMatchingProvenanceOutsideRun(ctx, models.ABSImportRunEntity{LocalID: 1}, runID); ok || err != nil {
		t.Fatalf("hasMatchingProvenanceOutsideRun without repo = %v %v", ok, err)
	}
	if rank := rollbackEntityRank(models.ABSImportRunEntity{EntityType: "mystery"}); rank != 5 {
		t.Fatalf("rank for unknown type = %d, want 5", rank)
	}
}
