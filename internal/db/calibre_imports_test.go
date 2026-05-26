package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestCalibreImportRunRepo_CreateFinishGet(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewCalibreImportRunRepo(database)
	run := &models.CalibreImportRun{
		LibraryPath: "/lib",
		Status:      "running",
	}
	if err := repo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("Create did not assign id")
	}
	if run.SourceID != "default" {
		t.Errorf("SourceID = %q, want default", run.SourceID)
	}

	if err := repo.Finish(context.Background(), run.ID, "completed", map[string]any{"booksAdded": 5}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	stored, err := repo.GetByID(context.Background(), run.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetByID: %v / %v", err, stored)
	}
	if stored.Status != "completed" {
		t.Errorf("status = %q, want completed", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Error("FinishedAt nil after Finish")
	}
	if stored.SummaryJSON == "" || stored.SummaryJSON == "{}" {
		t.Errorf("summary_json = %q, want non-empty", stored.SummaryJSON)
	}
}

func TestCalibreImportRunRepo_UpdateStatus(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewCalibreImportRunRepo(database)
	run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "running"}
	if err := repo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), run.ID, "rolled_back"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	stored, _ := repo.GetByID(context.Background(), run.ID)
	if stored == nil || stored.Status != "rolled_back" {
		t.Errorf("status = %+v, want rolled_back", stored)
	}
}

func TestCalibreImportRunRepo_ListRecent_OrderedDesc(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewCalibreImportRunRepo(database)
	for i := 0; i < 3; i++ {
		if err := repo.Create(context.Background(), &models.CalibreImportRun{
			LibraryPath: "/lib",
			Status:      "running",
		}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	runs, err := repo.ListRecent(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3", len(runs))
	}
	if runs[0].ID < runs[1].ID || runs[1].ID < runs[2].ID {
		t.Errorf("expected DESC id ordering, got %d,%d,%d", runs[0].ID, runs[1].ID, runs[2].ID)
	}
}

func TestCalibreProvenanceRepo_UpsertGetDelete(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	runs := NewCalibreImportRunRepo(database)
	parent := &models.CalibreImportRun{LibraryPath: "/lib", Status: "running"}
	if err := runs.Create(context.Background(), parent); err != nil {
		t.Fatalf("create parent run: %v", err)
	}

	repo := NewCalibreProvenanceRepo(database)
	runID := parent.ID
	p := &models.CalibreProvenance{
		EntityType:  "book",
		ExternalID:  "book:1",
		LocalID:     42,
		ImportRunID: &runID,
	}
	if err := repo.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.GetByExternal(context.Background(), "default", "book", "book:1")
	if err != nil || got == nil {
		t.Fatalf("GetByExternal: %v / %v", err, got)
	}
	if got.LocalID != 42 {
		t.Errorf("local_id = %d, want 42", got.LocalID)
	}

	// Re-upsert with a different local_id; the conflict clause should overwrite.
	p.LocalID = 99
	if err := repo.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert overwrite: %v", err)
	}
	got, _ = repo.GetByExternal(context.Background(), "default", "book", "book:1")
	if got == nil || got.LocalID != 99 {
		t.Errorf("after overwrite local_id = %+v, want 99", got)
	}

	// Delete via local; provenance row should be gone.
	n, err := repo.DeleteByLocal(context.Background(), "book", 99)
	if err != nil || n != 1 {
		t.Fatalf("DeleteByLocal: n=%d err=%v", n, err)
	}
	if got, _ := repo.GetByExternal(context.Background(), "default", "book", "book:1"); got != nil {
		t.Errorf("provenance still present after delete: %+v", got)
	}
}

func TestCalibreEntitySnapshotRepo_RecordRetainsFirstBefore(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	runs := NewCalibreImportRunRepo(database)
	snapshots := NewCalibreEntitySnapshotRepo(database)
	run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "running"}
	if err := runs.Create(context.Background(), run); err != nil {
		t.Fatalf("Create run: %v", err)
	}

	// First snapshot includes a "before" payload.
	first := &models.CalibreEntitySnapshot{
		RunID:        run.ID,
		EntityType:   "book",
		ExternalID:   "book:1",
		LocalID:      5,
		Outcome:      "updated",
		MetadataJSON: `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"title":"original"},"after":{"title":"first-mutation"}}}`,
	}
	if err := snapshots.Record(context.Background(), first); err != nil {
		t.Fatalf("Record first: %v", err)
	}

	// Second snapshot for the same entity changes "after" and tries to
	// overwrite "before" — the repo must keep the original "before".
	second := &models.CalibreEntitySnapshot{
		RunID:        run.ID,
		EntityType:   "book",
		ExternalID:   "book:1",
		LocalID:      5,
		Outcome:      "updated",
		MetadataJSON: `{"kind":"calibre_run_entity_metadata","version":1,"snapshot":{"entityType":"book","before":{"title":"wrong"},"after":{"title":"second-mutation"}}}`,
	}
	if err := snapshots.Record(context.Background(), second); err != nil {
		t.Fatalf("Record second: %v", err)
	}

	rows, err := snapshots.ListByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got %d", len(rows))
	}
	merged := rows[0].MetadataJSON
	if !contains(merged, `"original"`) {
		t.Errorf("merged metadata lost the original 'before': %s", merged)
	}
	if !contains(merged, `"second-mutation"`) {
		t.Errorf("merged metadata missing the latest 'after': %s", merged)
	}
}

// CreatedOutcomeWins exercises the outcome-merge rule: a later "updated" must
// not downgrade a prior "created" because rollback uses outcome to decide
// delete-vs-restore.
func TestCalibreEntitySnapshotRepo_CreatedOutcomeWins(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	runs := NewCalibreImportRunRepo(database)
	snapshots := NewCalibreEntitySnapshotRepo(database)
	run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "running"}
	if err := runs.Create(context.Background(), run); err != nil {
		t.Fatalf("Create run: %v", err)
	}

	if err := snapshots.Record(context.Background(), &models.CalibreEntitySnapshot{
		RunID: run.ID, EntityType: "book", ExternalID: "book:1", LocalID: 1, Outcome: "created",
	}); err != nil {
		t.Fatalf("Record created: %v", err)
	}
	if err := snapshots.Record(context.Background(), &models.CalibreEntitySnapshot{
		RunID: run.ID, EntityType: "book", ExternalID: "book:1", LocalID: 1, Outcome: "updated",
	}); err != nil {
		t.Fatalf("Record updated: %v", err)
	}
	rows, _ := snapshots.ListByRun(context.Background(), run.ID)
	if len(rows) != 1 || rows[0].Outcome != "created" {
		t.Errorf("outcome = %+v, want created (created must not be downgraded by updated)", rows)
	}
}

func contains(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
