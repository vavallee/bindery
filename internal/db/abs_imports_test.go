package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestABSImportRunRepoFinishRetainsCheckpointOnFailure(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewABSImportRunRepo(database)
	run := &models.ABSImportRun{
		SourceID:       "default",
		SourceLabel:    "Shelf",
		BaseURL:        "https://abs.example.com",
		LibraryID:      "lib-books",
		Status:         "running",
		CheckpointJSON: `{"libraryId":"lib-books","page":1}`,
	}
	if err := repo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.UpdateCheckpoint(context.Background(), run.ID, map[string]any{
		"libraryId": "lib-books",
		"page":      1,
	}); err != nil {
		t.Fatalf("UpdateCheckpoint: %v", err)
	}
	if err := repo.Finish(context.Background(), run.ID, "failed", map[string]any{"error": "boom"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	stored, err := repo.GetByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored run")
		return
	}
	if stored.CheckpointJSON == "" || stored.CheckpointJSON == "{}" {
		t.Fatalf("checkpoint_json = %q, want persisted checkpoint", stored.CheckpointJSON)
	}
}

func TestABSImportRunRepoUpdateStatus(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewABSImportRunRepo(database)
	run := &models.ABSImportRun{
		SourceID:    "default",
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      "completed",
	}
	if err := repo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.UpdateCheckpoint(context.Background(), run.ID, map[string]any{
		"libraryId": "lib-books",
		"page":      3,
	}); err != nil {
		t.Fatalf("UpdateCheckpoint: %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), run.ID, "rolled_back"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	stored, err := repo.GetByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.Status != "rolled_back" {
		t.Fatalf("run = %+v, want rolled_back status", stored)
	}
	if stored.CheckpointJSON != "{}" {
		t.Fatalf("checkpoint_json = %q, want cleared on rolled_back", stored.CheckpointJSON)
	}
}

func TestABSImportRunEntityRepoRecordPreservesCreatedOutcome(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	runRepo := NewABSImportRunRepo(database)
	run := &models.ABSImportRun{
		SourceID:    "default",
		SourceLabel: "Shelf",
		BaseURL:     "https://abs.example.com",
		LibraryID:   "lib-books",
		Status:      "completed",
	}
	if err := runRepo.Create(ctx, run); err != nil {
		t.Fatalf("Create run: %v", err)
	}
	repo := NewABSImportRunEntityRepo(database)
	entity := &models.ABSImportRunEntity{
		RunID:        run.ID,
		SourceID:     "default",
		LibraryID:    "lib-books",
		ItemID:       "li-first",
		EntityType:   "author",
		ExternalID:   "author-repeat",
		LocalID:      42,
		Outcome:      "created",
		MetadataJSON: `{"data":{"first":true}}`,
	}
	if err := repo.Record(ctx, entity); err != nil {
		t.Fatalf("Record created: %v", err)
	}
	entity.ItemID = "li-second"
	entity.Outcome = "linked"
	entity.MetadataJSON = `{"data":{"second":true}}`
	if err := repo.Record(ctx, entity); err != nil {
		t.Fatalf("Record linked: %v", err)
	}
	entity.Outcome = "updated"
	entity.MetadataJSON = `{"data":{"third":true}}`
	if err := repo.Record(ctx, entity); err != nil {
		t.Fatalf("Record updated: %v", err)
	}

	entities, err := repo.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("entities = %+v, want one merged row", entities)
	}
	if entities[0].Outcome != "created" {
		t.Fatalf("outcome = %q, want created", entities[0].Outcome)
	}
	for _, want := range []string{`"first":true`, `"second":true`, `"third":true`} {
		if !strings.Contains(entities[0].MetadataJSON, want) {
			t.Fatalf("metadata_json = %s, want merged key %s", entities[0].MetadataJSON, want)
		}
	}
}

func TestABSReviewItemRepoResolveAuthorForPrimaryRecordsAlias(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := NewAuthorRepo(database)
	aliasRepo := NewAuthorAliasRepo(database)
	author := &models.Author{
		ForeignID:        "OL-ANDY",
		Name:             "Andy Weir",
		SortName:         "Weir, Andy",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("Create author: %v", err)
	}
	repo := NewABSReviewItemRepo(database)
	if err := repo.UpsertPending(ctx, &models.ABSReviewItem{
		SourceID:      "default",
		LibraryID:     "lib-books",
		ItemID:        "li-weir",
		Title:         "Project Hail Mary",
		PrimaryAuthor: "Weir, Andy",
		ReviewReason:  "ambiguous_author",
		PayloadJSON:   "{}",
	}); err != nil {
		t.Fatalf("UpsertPending: %v", err)
	}

	updated, err := repo.ResolveAuthorForPrimary(ctx, "default", "lib-books", "Weir, Andy", "OL-ANDY", "Andy Weir")
	if err != nil {
		t.Fatalf("ResolveAuthorForPrimary: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	aliases, err := aliasRepo.ListByAuthor(ctx, author.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(aliases) != 1 || aliases[0].Name != "Weir, Andy" {
		t.Fatalf("aliases = %+v, want reviewer-resolved alias", aliases)
	}
}

func TestABSImportRunRepoLatestRunningWithCheckpoint(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewABSImportRunRepo(database)
	emptyCheckpoint := &models.ABSImportRun{
		SourceID:       "default",
		SourceLabel:    "Shelf",
		BaseURL:        "https://abs.example.com",
		LibraryID:      "lib-books",
		Status:         "running",
		CheckpointJSON: "{}",
	}
	if err := repo.Create(context.Background(), emptyCheckpoint); err != nil {
		t.Fatalf("Create emptyCheckpoint: %v", err)
	}
	resumable := &models.ABSImportRun{
		SourceID:       "default",
		SourceLabel:    "Shelf",
		BaseURL:        "https://abs.example.com",
		LibraryID:      "lib-books",
		Status:         "running",
		CheckpointJSON: `{"libraryId":"lib-books","page":2}`,
	}
	if err := repo.Create(context.Background(), resumable); err != nil {
		t.Fatalf("Create resumable: %v", err)
	}
	completed := &models.ABSImportRun{
		SourceID:       "default",
		SourceLabel:    "Shelf",
		BaseURL:        "https://abs.example.com",
		LibraryID:      "lib-books",
		Status:         "completed",
		CheckpointJSON: `{"libraryId":"lib-books","page":3}`,
	}
	if err := repo.Create(context.Background(), completed); err != nil {
		t.Fatalf("Create completed: %v", err)
	}

	got, err := repo.LatestRunningWithCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LatestRunningWithCheckpoint: %v", err)
	}
	if got == nil || got.ID != resumable.ID {
		t.Fatalf("run = %+v, want resumable run %d", got, resumable.ID)
	}
	if !strings.Contains(got.CheckpointJSON, `"page":2`) {
		t.Fatalf("checkpoint_json = %q, want page 2", got.CheckpointJSON)
	}
}

func TestABSReviewItemRepoMarkResolvedByItemIDs(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewABSReviewItemRepo(database)

	for _, item := range []models.ABSReviewItem{
		{SourceID: "default", LibraryID: "lib-books", ItemID: "it-1", Title: "A", PrimaryAuthor: "X", PayloadJSON: "{}", ReviewReason: "unmatched_book"},
		{SourceID: "default", LibraryID: "lib-books", ItemID: "it-2", Title: "B", PrimaryAuthor: "X", PayloadJSON: "{}", ReviewReason: "unmatched_book"},
		{SourceID: "default", LibraryID: "lib-books", ItemID: "it-3", Title: "C", PrimaryAuthor: "X", PayloadJSON: "{}", ReviewReason: "unmatched_book"},
		{SourceID: "default", LibraryID: "lib-other", ItemID: "it-1", Title: "D", PrimaryAuthor: "X", PayloadJSON: "{}", ReviewReason: "unmatched_book"},
	} {
		item := item
		if err := repo.UpsertPending(ctx, &item); err != nil {
			t.Fatalf("UpsertPending: %v", err)
		}
	}

	// Manually dismiss it-2 to confirm we never touch non-pending rows.
	if _, err := database.ExecContext(ctx, `UPDATE abs_review_queue SET status = 'dismissed' WHERE item_id = 'it-2' AND library_id = 'lib-books'`); err != nil {
		t.Fatalf("manual dismiss: %v", err)
	}

	count, err := repo.MarkResolvedByItemIDs(ctx, "default", "lib-books", []string{"it-1", "it-2", "it-3", ""})
	if err != nil {
		t.Fatalf("MarkResolvedByItemIDs: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 (it-1 and it-3 in lib-books only; it-2 already dismissed)", count)
	}

	// Empty ID slice is a no-op, no error.
	count, err = repo.MarkResolvedByItemIDs(ctx, "default", "lib-books", nil)
	if err != nil {
		t.Fatalf("empty MarkResolvedByItemIDs: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty count = %d, want 0", count)
	}

	// Other library kept untouched.
	pending, err := repo.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(pending) != 1 || pending[0].LibraryID != "lib-other" {
		t.Fatalf("pending = %+v, want only lib-other untouched", pending)
	}
}

func TestABSReviewItemRepoDismissByRunID(t *testing.T) {
	t.Parallel()

	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewABSReviewItemRepo(database)

	for i, runID := range []int64{1, 1, 2, 0} {
		item := &models.ABSReviewItem{
			SourceID:      "default",
			LibraryID:     "lib-books",
			ItemID:        "it-" + string(rune('a'+i)),
			PayloadJSON:   "{}",
			ReviewReason:  "unmatched_book",
			PrimaryAuthor: "X",
		}
		if runID != 0 {
			rid := runID
			item.LatestRunID = &rid
		}
		if err := repo.UpsertPending(ctx, item); err != nil {
			t.Fatalf("UpsertPending: %v", err)
		}
		if runID != 0 {
			if _, err := database.ExecContext(ctx, `UPDATE abs_review_queue SET latest_run_id = ? WHERE id = ?`, runID, item.ID); err != nil {
				t.Fatalf("set latest_run_id: %v", err)
			}
		}
	}

	count, err := repo.DismissByRunID(ctx, 1)
	if err != nil {
		t.Fatalf("DismissByRunID: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	count, err = repo.DismissByRunID(ctx, 0)
	if err != nil || count != 0 {
		t.Fatalf("DismissByRunID(0) = %d err=%v, want 0/nil", count, err)
	}
	count, err = repo.DismissByRunID(ctx, 999)
	if err != nil || count != 0 {
		t.Fatalf("DismissByRunID(unknown) = %d err=%v, want 0/nil", count, err)
	}
}
