package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate085AllowsBookFileAndKeepsRows covers the remaining half of #1635.
// Migration 072 widened both Calibre run-tracking CHECKs to allow
// 'series_link'; the book_files rows #2195 started registering need the same
// treatment, or their provenance is rejected and rollback never sees them.
// The rebuild must not lose the rows already in either table.
func TestMigrate085AllowsBookFileAndKeepsRows(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	runs := NewCalibreImportRunRepo(database)
	run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "completed"}
	if err := runs.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	restorePreBookFileCalibreTables(t, database)

	prov := NewCalibreProvenanceRepo(database)
	if err := prov.Upsert(ctx, &models.CalibreProvenance{
		SourceID: "default", EntityType: "series_link", ExternalID: "calibre:series-link:7:3", LocalID: 7, ImportRunID: &run.ID,
	}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	snaps := NewCalibreEntitySnapshotRepo(database)
	if err := snaps.Record(ctx, &models.CalibreEntitySnapshot{
		RunID: run.ID, SourceID: "default", EntityType: "series_link",
		ExternalID: "calibre:series-link:7:3", LocalID: 7, Outcome: "created",
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Sanity: the pre-085 constraint really does reject book_file.
	if err := prov.Upsert(ctx, &models.CalibreProvenance{
		SourceID: "default", EntityType: "book_file", ExternalID: "calibre:book-file:/lib/x.epub", LocalID: 7,
	}); err == nil {
		t.Fatal("pre-fix schema accepted a book_file row; the fixture is not reproducing migration 072")
	}

	v085 := migrationVersionForTest(t, "085_calibre_provenance_book_file.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v085); err != nil {
		t.Fatalf("clear migration 085 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 085: %v", err)
	}

	got, err := prov.GetByExternal(ctx, "default", "series_link", "calibre:series-link:7:3")
	if err != nil {
		t.Fatalf("GetByExternal after migration: %v", err)
	}
	if got == nil {
		t.Fatal("provenance row lost by the migration 085 rebuild")
	}
	if got.LocalID != 7 || got.ImportRunID == nil || *got.ImportRunID != run.ID {
		t.Errorf("provenance row mangled by rebuild: %+v", got)
	}
	list, err := snaps.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByRun after migration: %v", err)
	}
	if len(list) != 1 || list[0].EntityType != "series_link" || list[0].Outcome != "created" {
		t.Errorf("snapshot rows after rebuild = %+v, want the seeded series_link row", list)
	}

	// book_file is now accepted on both tables.
	if err := prov.Upsert(ctx, &models.CalibreProvenance{
		SourceID: "default", EntityType: "book_file",
		ExternalID: "calibre:book-file:/lib/x.epub", LocalID: 7, ImportRunID: &run.ID,
	}); err != nil {
		t.Fatalf("book_file provenance rejected after migration 085: %v", err)
	}
	if err := snaps.Record(ctx, &models.CalibreEntitySnapshot{
		RunID: run.ID, SourceID: "default", EntityType: "book_file",
		ExternalID: "calibre:book-file:/lib/x.epub", LocalID: 7, Outcome: "created",
	}); err != nil {
		t.Fatalf("book_file snapshot rejected after migration 085: %v", err)
	}

	for _, idx := range []string{"idx_calibre_provenance_local", "idx_calibre_entity_snapshots_run"} {
		var name string
		if err := database.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name); err != nil {
			t.Errorf("index %s missing after migration 085: %v", idx, err)
		}
	}
}

// restorePreBookFileCalibreTables recreates the two Calibre run-tracking tables
// with the migration-072 CHECK constraint (author/book/edition/series_link), so
// a test can exercise migration 085 as a real upgrade rather than a no-op
// rerun. Both tables are empty at this point, so no data is copied.
func restorePreBookFileCalibreTables(t *testing.T, database *sql.DB) {
	t.Helper()
	const preFix = `'author', 'book', 'edition', 'series_link'`
	stmts := []string{
		`DROP INDEX IF EXISTS idx_calibre_provenance_local`,
		`DROP TABLE calibre_provenance`,
		`CREATE TABLE calibre_provenance (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id     TEXT     NOT NULL DEFAULT 'default',
			entity_type   TEXT     NOT NULL CHECK(entity_type IN (` + preFix + `)),
			external_id   TEXT     NOT NULL,
			local_id      INTEGER  NOT NULL,
			import_run_id INTEGER  REFERENCES calibre_import_runs(id) ON DELETE SET NULL,
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (source_id, entity_type, external_id)
		)`,
		`CREATE INDEX idx_calibre_provenance_local ON calibre_provenance(entity_type, local_id)`,
		`DROP INDEX IF EXISTS idx_calibre_entity_snapshots_run`,
		`DROP TABLE calibre_entity_snapshots`,
		`CREATE TABLE calibre_entity_snapshots (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id        INTEGER  NOT NULL REFERENCES calibre_import_runs(id) ON DELETE CASCADE,
			source_id     TEXT     NOT NULL DEFAULT 'default',
			entity_type   TEXT     NOT NULL CHECK(entity_type IN (` + preFix + `)),
			external_id   TEXT     NOT NULL,
			local_id      INTEGER  NOT NULL DEFAULT 0,
			outcome       TEXT     NOT NULL DEFAULT '',
			metadata_json TEXT     NOT NULL DEFAULT '{}',
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (run_id, entity_type, external_id, local_id)
		)`,
		`CREATE INDEX idx_calibre_entity_snapshots_run ON calibre_entity_snapshots(run_id, entity_type, local_id)`,
	}
	for _, stmt := range stmts {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("restore pre-book_file calibre schema: %v\nSQL: %s", err, stmt)
		}
	}
}
