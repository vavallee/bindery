package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestABSRepos_Coverage covers the ABS metadata conflict, provenance, run
// and review queue repos on one shared in memory database. Each subtest uses
// its own keys so they can run in sequence without interfering.
func TestABSRepos_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	t.Run("metadata conflicts", func(t *testing.T) {
		repo := NewABSMetadataConflictRepo(database)

		if got, err := repo.GetByID(ctx, 12345); err != nil || got != nil {
			t.Fatalf("GetByID(missing) = %+v, %v; want nil,nil", got, err)
		}
		if got, err := repo.GetByEntityField(ctx, "book", 1, "title"); err != nil || got != nil {
			t.Fatalf("GetByEntityField(missing) = %+v, %v; want nil,nil", got, err)
		}

		mk := func(entity string, local int64, field, absValue string) *models.ABSMetadataConflict {
			t.Helper()
			c := &models.ABSMetadataConflict{
				LibraryID: "lib", ItemID: "item", EntityType: entity, LocalID: local,
				FieldName: field, ABSValue: absValue, UpstreamValue: "up", AppliedSource: "abs",
			}
			if err := repo.Upsert(ctx, c); err != nil {
				t.Fatalf("Upsert %s/%d/%s: %v", entity, local, field, err)
			}
			return c
		}
		a := mk("book", 1, "title", "ABS Title")
		if a.ID == 0 || a.SourceID != "default" || a.ResolutionStatus != "unresolved" || a.CreatedAt.IsZero() {
			t.Fatalf("Upsert defaults not applied: %+v", a)
		}
		b := mk("book", 1, "description", "desc")
		c := mk("author", 2, "name", "Name")

		// Re-upsert on the same entity+field updates in place.
		again := mk("book", 1, "title", "ABS Title v2")
		if again.ID != a.ID {
			t.Fatalf("re-upsert id = %d, want %d", again.ID, a.ID)
		}
		got, err := repo.GetByEntityField(ctx, "book", 1, "title")
		if err != nil || got == nil || got.ABSValue != "ABS Title v2" {
			t.Fatalf("GetByEntityField = %+v, %v", got, err)
		}

		// CHECK constraints: entity_type and resolution_status are enumerated.
		if err := repo.Upsert(ctx, &models.ABSMetadataConflict{EntityType: "series", LocalID: 3, FieldName: "x"}); err == nil {
			t.Error("Upsert with entity_type=series should violate the CHECK constraint")
		}
		if err := repo.Upsert(ctx, &models.ABSMetadataConflict{EntityType: "book", LocalID: 3, FieldName: "x", ResolutionStatus: "bogus"}); err == nil {
			t.Error("Upsert with an unknown resolution_status should violate the CHECK constraint")
		}

		// Claim moves unresolved to resolving exactly once; Unclaim reverts.
		ok, err := repo.Claim(ctx, b.ID)
		if err != nil || !ok {
			t.Fatalf("Claim = %v, %v; want true", ok, err)
		}
		if ok, err := repo.Claim(ctx, b.ID); err != nil || ok {
			t.Fatalf("second Claim = %v, %v; want false", ok, err)
		}
		if ok, err := repo.Claim(ctx, 999999); err != nil || ok {
			t.Fatalf("Claim(missing) = %v, %v; want false", ok, err)
		}
		if g, _ := repo.GetByID(ctx, b.ID); g == nil || g.ResolutionStatus != "resolving" {
			t.Fatalf("status after Claim = %+v, want resolving", g)
		}
		if err := repo.Unclaim(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if g, _ := repo.GetByID(ctx, b.ID); g == nil || g.ResolutionStatus != "unresolved" {
			t.Fatalf("status after Unclaim = %+v, want unresolved", g)
		}

		// Mark c resolved: List puts unresolved rows first.
		c.ResolutionStatus = "resolved"
		if err := repo.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
		// A resolved conflict cannot be claimed.
		if ok, err := repo.Claim(ctx, c.ID); err != nil || ok {
			t.Fatalf("Claim(resolved) = %v, %v; want false", ok, err)
		}
		list, err := repo.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 3 || list[2].ID != c.ID {
			t.Fatalf("List = %+v, want 3 rows with the resolved one last", list)
		}
		page, total, err := repo.ListPaginated(ctx, 1, 1)
		if err != nil || total != 3 || len(page) != 1 || page[0].ID != list[1].ID {
			t.Fatalf("ListPaginated(1,1) = %+v total=%d err=%v; want the second row of %+v", page, total, err, list)
		}
		page, total, err = repo.ListPaginated(ctx, 2, 0)
		if err != nil || total != 3 || len(page) != 2 {
			t.Fatalf("ListPaginated(2,0) = %d rows total=%d err=%v", len(page), total, err)
		}
	})

	t.Run("provenance", func(t *testing.T) {
		runs := NewABSImportRunRepo(database)
		run := &models.ABSImportRun{LibraryID: "lib-prov", Status: "running"}
		if err := runs.Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		repo := NewABSProvenanceRepo(database)
		if got, err := repo.GetByExternal(ctx, "default", "lib-prov", "book", "nope"); err != nil || got != nil {
			t.Fatalf("GetByExternal(missing) = %+v, %v", got, err)
		}

		p := &models.ABSProvenance{
			LibraryID: "lib-prov", EntityType: "book", ExternalID: "ext-1", LocalID: 50,
			ItemID: "li-1", Format: "audiobook", FileIDs: []string{"f1", "f2"}, ImportRunID: &run.ID,
		}
		if err := repo.Upsert(ctx, p); err != nil {
			t.Fatal(err)
		}
		if p.ID == 0 || p.SourceID != "default" {
			t.Fatalf("Upsert did not reload id/default source: %+v", p)
		}
		firstID := p.ID
		p.LocalID = 51
		p.FileIDs = []string{"f3"}
		if err := repo.Upsert(ctx, p); err != nil {
			t.Fatal(err)
		}
		if p.ID != firstID {
			t.Fatalf("re-upsert id = %d, want %d", p.ID, firstID)
		}
		got, err := repo.GetByExternal(ctx, "default", "lib-prov", "book", "ext-1")
		if err != nil || got == nil {
			t.Fatalf("GetByExternal = %+v, %v", got, err)
		}
		if got.LocalID != 51 || len(got.FileIDs) != 1 || got.FileIDs[0] != "f3" || got.ImportRunID == nil || *got.ImportRunID != run.ID {
			t.Fatalf("GetByExternal = %+v, want local 51, file ids [f3], run %d", got, run.ID)
		}

		for _, ext := range []string{"ext-2", "ext-3"} {
			if err := repo.Upsert(ctx, &models.ABSProvenance{LibraryID: "lib-prov", EntityType: "book", ExternalID: ext, LocalID: 51}); err != nil {
				t.Fatal(err)
			}
		}
		if err := repo.Upsert(ctx, &models.ABSProvenance{LibraryID: "lib-prov", EntityType: "author", ExternalID: "ext-a", LocalID: 51}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Upsert(ctx, &models.ABSProvenance{LibraryID: "lib-prov", EntityType: "podcast", ExternalID: "x", LocalID: 1}); err == nil {
			t.Error("Upsert with entity_type=podcast should violate the CHECK constraint")
		}

		list, err := repo.ListByLocal(ctx, "book", 51)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 3 || list[0].ExternalID != "ext-1" || list[2].ExternalID != "ext-3" {
			t.Fatalf("ListByLocal(book,51) = %+v, want ext-1..ext-3 in id order", list)
		}
		// nil FileIDs is stored as JSON null and decodes back to nil.
		if list[1].FileIDs != nil {
			t.Errorf("FileIDs for a nil slice = %v, want nil", list[1].FileIDs)
		}

		if err := repo.DeleteByExternal(ctx, "default", "lib-prov", "book", "ext-2"); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.GetByExternal(ctx, "default", "lib-prov", "book", "ext-2"); got != nil {
			t.Fatalf("ext-2 still present after DeleteByExternal: %+v", got)
		}
		if n, err := repo.DeleteByLocal(ctx, "book", 0); err != nil || n != 0 {
			t.Fatalf("DeleteByLocal(0) = %d, %v; want a no-op", n, err)
		}
		n, err := repo.DeleteByLocal(ctx, "book", 51)
		if err != nil || n != 2 {
			t.Fatalf("DeleteByLocal(book,51) = %d, %v; want 2", n, err)
		}
		// The author row with the same local id is untouched.
		if list, _ := repo.ListByLocal(ctx, "author", 51); len(list) != 1 {
			t.Fatalf("author provenance = %+v, want it kept", list)
		}

		// A corrupt file_ids_json surfaces as a decode error, not a silent nil.
		if _, err := database.ExecContext(ctx, `UPDATE abs_provenance SET file_ids_json = 'not-json' WHERE external_id = 'ext-a'`); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.GetByExternal(ctx, "default", "lib-prov", "author", "ext-a"); err == nil {
			t.Error("GetByExternal with corrupt file_ids_json should return an error")
		}
		if _, err := repo.ListByLocal(ctx, "author", 51); err == nil {
			t.Error("ListByLocal with corrupt file_ids_json should return an error")
		}
	})

	t.Run("runs", func(t *testing.T) {
		repo := NewABSImportRunRepo(database)
		if got, err := repo.GetByID(ctx, 999999); err != nil || got != nil {
			t.Fatalf("GetByID(missing) = %+v, %v", got, err)
		}
		// id 0 is a no-op for every mutator.
		if err := repo.Finish(ctx, 0, "completed", nil); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateStatus(ctx, 0, "failed"); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateCheckpoint(ctx, 0, map[string]int{"page": 1}); err != nil {
			t.Fatal(err)
		}

		var ids []int64
		for i, lib := range []string{"lib-r1", "lib-r2", "lib-r3"} {
			run := &models.ABSImportRun{LibraryID: lib, Status: "running", DryRun: i == 1, SummaryJSON: "{}"}
			if err := repo.Create(ctx, run); err != nil {
				t.Fatal(err)
			}
			if run.SourceConfigJSON != "{}" || run.CheckpointJSON != "{}" {
				t.Fatalf("Create defaults = %q/%q, want {}", run.SourceConfigJSON, run.CheckpointJSON)
			}
			ids = append(ids, run.ID)
		}
		base := time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC) // after the provenance subtest's run
		for i, id := range ids {
			if _, err := database.ExecContext(ctx, `UPDATE abs_import_runs SET started_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Minute), id); err != nil {
				t.Fatal(err)
			}
		}

		recent, err := repo.ListRecent(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(recent) != 2 || recent[0].ID != ids[2] || recent[1].ID != ids[1] || !recent[1].DryRun {
			t.Fatalf("ListRecent(2) = %+v, want newest two (ids %d,%d) with the dry run flag", recent, ids[2], ids[1])
		}
		// limit <= 0 falls back to 10 and so returns at least our three.
		all, err := repo.ListRecent(ctx, 0)
		if err != nil || len(all) < 3 {
			t.Fatalf("ListRecent(0) = %d rows, %v", len(all), err)
		}

		// A summary that cannot be marshalled is rejected before touching the row.
		if err := repo.Finish(ctx, ids[0], "completed", func() {}); err == nil {
			t.Error("Finish with an unmarshalable summary should fail")
		}
		if err := repo.UpdateCheckpoint(ctx, ids[0], make(chan int)); err == nil {
			t.Error("UpdateCheckpoint with an unmarshalable checkpoint should fail")
		}
		if err := repo.Finish(ctx, ids[0], "failed", nil); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetByID(ctx, ids[0])
		if err != nil || got == nil || got.Status != "failed" || got.SummaryJSON != "{}" || got.FinishedAt == nil {
			t.Fatalf("after Finish(failed, nil) = %+v, %v", got, err)
		}
	})

	t.Run("review queue", func(t *testing.T) {
		repo := NewABSReviewItemRepo(database)
		if err := repo.UpsertPending(ctx, nil); err != nil {
			t.Fatalf("UpsertPending(nil) = %v, want nil", err)
		}
		if got, err := repo.GetByID(ctx, 999999); err != nil || got != nil {
			t.Fatalf("GetByID(missing) = %+v, %v", got, err)
		}
		var items []*models.ABSReviewItem
		for _, id := range []string{"rq-1", "rq-2", "rq-3"} {
			it := &models.ABSReviewItem{SourceID: "src", LibraryID: "lib-rq", ItemID: id, Title: "Title " + id, PrimaryAuthor: "Someone"}
			if err := repo.UpsertPending(ctx, it); err != nil {
				t.Fatal(err)
			}
			items = append(items, it)
		}
		base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		for i, it := range items {
			if _, err := database.ExecContext(ctx, `UPDATE abs_review_queue SET updated_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Minute), it.ID); err != nil {
				t.Fatal(err)
			}
		}

		if err := repo.ResolveBook(ctx, items[0].ID, " ", "x", ""); err == nil {
			t.Error("ResolveBook without a foreign id should fail")
		}
		if err := repo.ResolveBook(ctx, items[0].ID, "OL1W", "  Real Title ", " Edited "); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetByID(ctx, items[0].ID)
		if err != nil || got == nil {
			t.Fatalf("GetByID = %+v, %v", got, err)
		}
		if got.ResolvedBookForeignID != "OL1W" || got.ResolvedBookTitle != "Real Title" || got.EditedTitle != "Edited" {
			t.Fatalf("ResolveBook stored %+v", got)
		}

		if err := repo.UpdateStatus(ctx, items[1].ID, " approved "); err != nil {
			t.Fatal(err)
		}
		approved, err := repo.ListByStatus(ctx, "approved")
		if err != nil || len(approved) != 1 || approved[0].ID != items[1].ID {
			t.Fatalf("ListByStatus(approved) = %+v, %v", approved, err)
		}

		// ResolveBook bumped items[0] to now, so it is the newest pending row.
		page, total, err := repo.ListByStatusPaginated(ctx, "pending", 1, 1)
		if err != nil || total != 2 || len(page) != 1 || page[0].ID != items[2].ID {
			t.Fatalf("ListByStatusPaginated(pending,1,1) = %+v total=%d err=%v; want rq-3", page, total, err)
		}

		if _, err := repo.ResolveAuthorForPrimary(ctx, "src", "lib-rq", "Someone", "", "Name"); err == nil {
			t.Error("ResolveAuthorForPrimary without a foreign id should fail")
		}
		if _, err := repo.ResolveAuthorForPrimary(ctx, "src", "lib-rq", "  ", "OL1A", "Name"); err == nil {
			t.Error("ResolveAuthorForPrimary without a primary author should fail")
		}
		// Different library: nothing matches.
		if n, err := repo.ResolveAuthorForPrimary(ctx, "src", "other-lib", "Someone", "OL1A", "Someone Else"); err != nil || n != 0 {
			t.Fatalf("ResolveAuthorForPrimary(other lib) = %d, %v; want 0", n, err)
		}
		// Canonical author not created locally yet: rows update, no alias, no error.
		n, err := repo.ResolveAuthorForPrimary(ctx, "src", "lib-rq", "someone", "OL-NOT-LOCAL", "Someone Else")
		if err != nil || n != 2 {
			t.Fatalf("ResolveAuthorForPrimary = %d, %v; want the two pending rows", n, err)
		}
	})
}

// TestMergeABSSnapshotMetadata pins the snapshot merge rule used when one run
// records the same entity twice: the first "before" image wins, every other
// key takes the newer value.
func TestMergeABSSnapshotMetadata(t *testing.T) {
	t.Parallel()
	if got := mergeABSSnapshotMetadata(nil, map[string]any{"a": 1}); got["a"] != 1 {
		t.Errorf("empty existing = %v", got)
	}
	if got := mergeABSSnapshotMetadata(map[string]any{"a": 1}, nil); got["a"] != 1 {
		t.Errorf("empty incoming = %v", got)
	}
	got := mergeABSSnapshotMetadata(
		map[string]any{"before": "old", "after": "old"},
		map[string]any{"before": "new", "after": "new", "extra": true},
	)
	if got["before"] != "old" || got["after"] != "new" || got["extra"] != true {
		t.Errorf("merge = %v, want before kept, after and extra replaced", got)
	}

	merged := mergeABSRunEntityMetadata(
		`{"snapshot":{"before":"b1","after":"a1"},"data":{"x":1},"note":"first"}`,
		`{"snapshot":{"before":"b2","after":"a2"},"data":{"y":2},"note":"second"}`,
	)
	m := decodeJSONMap(merged)
	snap := asJSONMap(m["snapshot"])
	data := asJSONMap(m["data"])
	if snap["before"] != "b1" || snap["after"] != "a2" || data["x"] == nil || data["y"] == nil || m["note"] != "second" {
		t.Errorf("mergeABSRunEntityMetadata = %s", merged)
	}
	if got := mergeABSRunEntityMetadata(`{"a":1}`, ""); got != `{"a":1}` {
		t.Errorf("empty incoming = %s, want existing", got)
	}
	if got := mergeABSRunEntityMetadata("", "{}"); got != "{}" {
		t.Errorf("both empty = %s, want {}", got)
	}
	if got := mergeABSRunEntityMetadata("not json", `{"b":2}`); got != `{"b":2}` {
		t.Errorf("corrupt existing = %s, want the incoming map", got)
	}
	if asJSONMap("string") != nil || asJSONMap(nil) != nil {
		t.Error("asJSONMap should return nil for non-map values")
	}
	if got := encodeJSONMap(map[string]any{"f": func() {}}); got != "{}" {
		t.Errorf("encodeJSONMap(unmarshalable) = %s, want {}", got)
	}
}

// TestProwlarrRepo_Coverage covers CRUD, the last sync stamp and the boolean
// column mapping for prowlarr_instances.
func TestProwlarrRepo_Coverage(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewProwlarrRepo(database)

	if got, err := repo.GetByID(ctx, 1); err != nil || got != nil {
		t.Fatalf("GetByID(missing) = %+v, %v; want nil,nil", got, err)
	}
	if list, err := repo.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("List(empty) = %+v, %v", list, err)
	}

	a := &models.ProwlarrInstance{Name: "A", URL: "http://a", APIKey: "ka", SyncOnStartup: true, Enabled: true}
	b := &models.ProwlarrInstance{Name: "B", URL: "http://b", APIKey: "kb"}
	for _, p := range []*models.ProwlarrInstance{a, b} {
		if err := repo.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		if p.ID == 0 || p.CreatedAt.IsZero() {
			t.Fatalf("Create did not populate id/timestamps: %+v", p)
		}
	}
	// url is NOT NULL; a NULL there is a constraint error. The struct always
	// carries a string, so drive it through SQL to confirm the schema guard.
	if _, err := database.ExecContext(ctx, `INSERT INTO prowlarr_instances (url, created_at, updated_at) VALUES (NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err == nil {
		t.Error("NULL url should violate NOT NULL")
	}

	list, err := repo.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if !list[0].SyncOnStartup || !list[0].Enabled || list[1].SyncOnStartup || list[1].Enabled {
		t.Fatalf("boolean mapping wrong: %+v", list)
	}
	if list[0].LastSyncAt != nil {
		t.Fatalf("LastSyncAt before any sync = %v, want nil", list[0].LastSyncAt)
	}

	b.Name = "B2"
	b.Enabled = true
	before := b.UpdatedAt
	if err := repo.Update(ctx, b); err != nil {
		t.Fatal(err)
	}
	if b.UpdatedAt.Before(before) {
		t.Errorf("Update did not advance UpdatedAt")
	}
	got, err := repo.GetByID(ctx, b.ID)
	if err != nil || got == nil || got.Name != "B2" || !got.Enabled || got.APIKey != "kb" {
		t.Fatalf("GetByID after Update = %+v, %v", got, err)
	}

	syncAt := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	if err := repo.SetLastSyncAt(ctx, a.ID, syncAt); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, a.ID)
	if err != nil || got == nil || got.LastSyncAt == nil || !got.LastSyncAt.Equal(syncAt) {
		t.Fatalf("LastSyncAt = %+v, %v; want %v", got, err, syncAt)
	}

	// Deleting an instance nulls the indexers that pointed at it.
	if _, err := database.ExecContext(ctx, `INSERT INTO indexers (name, url, api_key, prowlarr_instance_id) VALUES ('ix', 'http://ix', '', ?)`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetByID(ctx, a.ID); got != nil {
		t.Fatalf("instance still present after Delete: %+v", got)
	}
	var linked int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM indexers WHERE prowlarr_instance_id IS NOT NULL`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 0 {
		t.Errorf("indexers still linked to a deleted instance: %d", linked)
	}
}
