package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestDownloadRepo_TorrentIDRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	dl := &models.Download{
		GUID: "torrent-guid", Title: "torrent.release", NZBURL: "magnet:?xt=urn:btih:abc",
		Size: 2048, Status: models.StateGrabbed, Protocol: "torrent",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	// GetByTorrentID — not found before SetTorrentID.
	got, err := repo.GetByTorrentID(ctx, "hash123")
	if err != nil {
		t.Fatalf("GetByTorrentID missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unset torrent_id, got %+v", got)
	}

	// Set and look up. Torrent hashes are normalized to lowercase at the
	// storage boundary so older mixed-case IDs still compare consistently.
	if err := repo.SetTorrentID(ctx, dl.ID, "HASH123"); err != nil {
		t.Fatalf("SetTorrentID: %v", err)
	}
	got, err = repo.GetByTorrentID(ctx, "hash123")
	if err != nil {
		t.Fatalf("GetByTorrentID: %v", err)
	}
	if got == nil || got.ID != dl.ID {
		t.Errorf("GetByTorrentID unexpected: %+v", got)
	}
	if got.TorrentID == nil || *got.TorrentID != "hash123" {
		t.Errorf("TorrentID not populated: %v", got.TorrentID)
	}
	got, err = repo.GetByTorrentID(ctx, "HASH123")
	if err != nil {
		t.Fatalf("GetByTorrentID uppercase: %v", err)
	}
	if got == nil || got.ID != dl.ID {
		t.Errorf("GetByTorrentID uppercase unexpected: %+v", got)
	}
}

func TestDownloadRepo_GetByNzoIDNotFound(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewDownloadRepo(database)

	got, err := repo.GetByNzoID(context.Background(), "missing-nzo")
	if err != nil {
		t.Fatalf("GetByNzoID missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing nzo_id, got %+v", got)
	}
}

// TestDownloadRepo_RecoverInterruptedImports verifies the startup sweep
// (issue #706 finding 1): downloads wedged mid-import in StateImporting /
// StateImportPending are moved to StateImportFailed so the retry path can pick
// them up, while terminal and unrelated states are left untouched.
func TestDownloadRepo_RecoverInterruptedImports(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	mk := func(guid string, status models.DownloadState) *models.Download {
		d := &models.Download{GUID: guid, Title: guid, NZBURL: "x", Status: status}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %s: %v", guid, err)
		}
		return d
	}

	importing := mk("wedged-importing", models.StateImporting)
	pending := mk("wedged-pending", models.StateImportPending)
	done := mk("terminal-imported", models.StateImported)
	external := mk("external-handoff", models.StateImportExternal)
	downloading := mk("still-downloading", models.StateDownloading)

	recovered, err := repo.RecoverInterruptedImports(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedImports: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered %d downloads, want 2 (importing + pending)", len(recovered))
	}

	check := func(id int64, want models.DownloadState) {
		all, err := repo.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range all {
			if d.ID == id {
				if d.Status != want {
					t.Errorf("download %d status = %q, want %q", id, d.Status, want)
				}
				return
			}
		}
		t.Errorf("download %d not found", id)
	}

	// The two wedged downloads must now be retryable.
	check(importing.ID, models.StateImportFailed)
	check(pending.ID, models.StateImportFailed)
	// Terminal / parked / in-flight states must be untouched. In particular
	// StateImportExternal is a legitimate long-lived parked state, NOT a crash
	// artefact, and must survive the sweep.
	check(done.ID, models.StateImported)
	check(external.ID, models.StateImportExternal)
	check(downloading.ID, models.StateDownloading)

	// Idempotent: a second sweep with nothing wedged recovers nothing.
	again, err := repo.RecoverInterruptedImports(ctx)
	if err != nil {
		t.Fatalf("second RecoverInterruptedImports: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep recovered %d downloads, want 0", len(again))
	}
}

// TestDownload_GrabbedAtAutoSet is the regression test for Wave 4 / finding
// 22: the StateGrabbed -> StateCompleted fast path (#769 duplicate-add)
// previously left grabbed_at NULL, hiding the row from the stall detector
// and leaving the queue UI's Grabbed column blank.
func TestDownload_GrabbedAtAutoSet(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	dl := &models.Download{
		GUID: "fast-path-guid", Title: "fast.path.release", NZBURL: "x",
		Size: 1, Status: models.StateGrabbed, Protocol: "torrent",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Fresh row has grabbed_at == NULL.
	got, err := repo.GetByGUID(ctx, dl.GUID)
	if err != nil || got == nil {
		t.Fatalf("get after create: %v", err)
	}
	if got.GrabbedAt != nil {
		t.Fatalf("grabbed_at should be NULL after Create, got %v", got.GrabbedAt)
	}

	// Direct Grabbed -> Completed hop. Before the fix this left grabbed_at NULL.
	if err := repo.UpdateStatus(ctx, dl.ID, models.StateCompleted); err != nil {
		t.Fatalf("UpdateStatus -> Completed: %v", err)
	}
	got, err = repo.GetByGUID(ctx, dl.GUID)
	if err != nil || got == nil {
		t.Fatalf("get after Completed: %v", err)
	}
	if got.GrabbedAt == nil {
		t.Fatalf("grabbed_at must be auto-populated on Completed transition (Wave 4 finding 22)")
	}
	if got.CompletedAt == nil {
		t.Errorf("completed_at must be populated too")
	}
}

// TestDownload_GrabbedAtPreservedOnHistoricalSet verifies that SetGrabbedAt
// (the historical-replay setter) wins over UpdateStatus's auto-default: when
// a caller has already written a specific historical timestamp, the
// auto-default must not clobber it on the next forward transition.
func TestDownload_GrabbedAtPreservedOnHistoricalSet(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	dl := &models.Download{
		GUID: "historical-guid", Title: "historical.release", NZBURL: "x",
		Size: 1, Status: models.StateGrabbed, Protocol: "torrent",
	}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A backup-replay tool sets the historical grabbed_at to a specific
	// past time before kicking the state machine forward.
	historical := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := repo.SetGrabbedAt(ctx, dl.ID, historical); err != nil {
		t.Fatalf("SetGrabbedAt: %v", err)
	}

	// Forward transition via the fast path. Must NOT overwrite the historical value.
	if err := repo.UpdateStatus(ctx, dl.ID, models.StateCompleted); err != nil {
		t.Fatalf("UpdateStatus -> Completed: %v", err)
	}
	got, err := repo.GetByGUID(ctx, dl.GUID)
	if err != nil || got == nil {
		t.Fatalf("get after Completed: %v", err)
	}
	if got.GrabbedAt == nil {
		t.Fatalf("grabbed_at unexpectedly NULL after SetGrabbedAt + UpdateStatus")
	}
	// sqlite stores the time round-tripped through string parsing; compare via Equal.
	if !got.GrabbedAt.Equal(historical) {
		t.Errorf("grabbed_at clobbered: want %v, got %v (SetGrabbedAt was overwritten by UpdateStatus auto-default)",
			historical, *got.GrabbedAt)
	}

	// Downloading transition (which also writes grabbed_at) must also preserve.
	dl2 := &models.Download{
		GUID: "historical-guid-2", Title: "h2", NZBURL: "x",
		Size: 1, Status: models.StateGrabbed, Protocol: "torrent",
	}
	if err := repo.Create(ctx, dl2); err != nil {
		t.Fatalf("create dl2: %v", err)
	}
	if err := repo.SetGrabbedAt(ctx, dl2.ID, historical); err != nil {
		t.Fatalf("SetGrabbedAt dl2: %v", err)
	}
	if err := repo.UpdateStatus(ctx, dl2.ID, models.StateDownloading); err != nil {
		t.Fatalf("UpdateStatus -> Downloading dl2: %v", err)
	}
	got2, err := repo.GetByGUID(ctx, dl2.GUID)
	if err != nil || got2 == nil {
		t.Fatalf("get dl2: %v", err)
	}
	if got2.GrabbedAt == nil || !got2.GrabbedAt.Equal(historical) {
		t.Errorf("Downloading transition clobbered historical grabbed_at: got %v", got2.GrabbedAt)
	}
}

// TestReconcile_RequeuesStuckCompleted is the Wave 4 / finding 21 regression
// test: a StateCompleted row whose book has no book_files entry is moved to
// StateImportPending so the normal scanner tick picks it up.
func TestReconcile_RequeuesStuckCompleted(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	dlRepo := NewDownloadRepo(database)

	stuck := &models.Download{
		GUID: "stuck-completed", Title: "stuck", NZBURL: "x",
		BookID: &book.ID, Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := dlRepo.Create(ctx, stuck); err != nil {
		t.Fatalf("create stuck: %v", err)
	}
	// Force status to Completed; Create only stores it raw, no migration required.
	if _, err := database.ExecContext(ctx,
		"UPDATE downloads SET status=? WHERE id=?", models.StateCompleted, stuck.ID); err != nil {
		t.Fatalf("force Completed: %v", err)
	}

	recovered, err := dlRepo.RecoverWedgedCompleted(ctx, 3, 0)
	if err != nil {
		t.Fatalf("RecoverWedgedCompleted: %v", err)
	}
	if len(recovered) != 1 || recovered[0] != stuck.ID {
		t.Fatalf("recovered = %v, want exactly [%d]", recovered, stuck.ID)
	}

	got, err := dlRepo.GetByGUID(ctx, stuck.GUID)
	if err != nil || got == nil {
		t.Fatalf("get stuck: %v", err)
	}
	if got.Status != models.StateImportPending {
		t.Errorf("status = %q, want %q", got.Status, models.StateImportPending)
	}
	if got.ImportRetryCount != 1 {
		t.Errorf("import_retry_count = %d, want 1 (bumped to enforce cap across restarts)", got.ImportRetryCount)
	}
}

// TestReconcile_SkipsCompletedWithBookFiles confirms that a Completed row
// whose import actually landed (book_files row exists for the book) is NOT
// re-queued. The reconciliation must be a no-op for already-imported books,
// so a process that crashed AFTER AddBookFile but BEFORE writing
// StateImported never re-copies the bytes.
func TestReconcile_SkipsCompletedWithBookFiles(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	dlRepo := NewDownloadRepo(database)
	bookRepo := NewBookRepo(database)

	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, "/lib/already-imported.epub"); err != nil {
		t.Fatalf("seed book_file: %v", err)
	}

	dl := &models.Download{
		GUID: "completed-with-file", Title: "ok", NZBURL: "x",
		BookID: &book.ID, Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		"UPDATE downloads SET status=? WHERE id=?", models.StateCompleted, dl.ID); err != nil {
		t.Fatalf("force Completed: %v", err)
	}

	recovered, err := dlRepo.RecoverWedgedCompleted(ctx, 3, 0)
	if err != nil {
		t.Fatalf("RecoverWedgedCompleted: %v", err)
	}
	if len(recovered) != 0 {
		t.Errorf("recovered = %v, want empty (book already has a book_file row)", recovered)
	}
}

// TestReconcile_CapsWorkPerTick verifies the per-call cap. Seeding cap+10 wedged
// rows must yield exactly cap recovered rows, so a first boot after upgrade
// with thousands of wedged downloads does not stampede the importer in a
// single sweep.
func TestReconcile_CapsWorkPerTick(t *testing.T) {
	database, _, _ := openTestDB(t)
	ctx := context.Background()
	dlRepo := NewDownloadRepo(database)

	const cap = 5
	const extra = 10
	for i := 0; i < cap+extra; i++ {
		dl := &models.Download{
			GUID: "wedged-" + time.Duration(i).String(), Title: "x", NZBURL: "x",
			Status: models.StateCompleted, Protocol: "torrent",
		}
		if err := dlRepo.Create(ctx, dl); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if _, err := database.ExecContext(ctx,
			"UPDATE downloads SET status=? WHERE id=?", models.StateCompleted, dl.ID); err != nil {
			t.Fatalf("force Completed %d: %v", i, err)
		}
	}

	recovered, err := dlRepo.RecoverWedgedCompleted(ctx, 3, cap)
	if err != nil {
		t.Fatalf("RecoverWedgedCompleted: %v", err)
	}
	if len(recovered) != cap {
		t.Errorf("recovered = %d, want exactly cap=%d", len(recovered), cap)
	}

	// Remaining wedged rows survive untouched for the next sweep.
	var remaining int
	if err := database.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM downloads WHERE status=?", models.StateCompleted).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != extra {
		t.Errorf("remaining Completed = %d, want %d (cap left them alone)", remaining, extra)
	}
}

// TestReconcile_SkipsExhaustedRetryBudget verifies that a row whose
// import_retry_count has reached importRetryLimit is NOT re-queued. The
// reconciliation must not loop forever on a permanently broken row across
// restart cycles.
func TestReconcile_SkipsExhaustedRetryBudget(t *testing.T) {
	database, _, book := openTestDB(t)
	ctx := context.Background()
	dlRepo := NewDownloadRepo(database)

	dl := &models.Download{
		GUID: "exhausted", Title: "x", NZBURL: "x",
		BookID: &book.ID, Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		"UPDATE downloads SET status=?, import_retry_count=? WHERE id=?",
		models.StateCompleted, 3, dl.ID); err != nil {
		t.Fatalf("force Completed + exhausted: %v", err)
	}

	recovered, err := dlRepo.RecoverWedgedCompleted(ctx, 3, 0)
	if err != nil {
		t.Fatalf("RecoverWedgedCompleted: %v", err)
	}
	if len(recovered) != 0 {
		t.Errorf("recovered = %v, want empty (retry budget exhausted)", recovered)
	}

	// The over-limit counter sees it.
	n, err := dlRepo.CountWedgedCompletedOverRetryLimit(ctx, 3)
	if err != nil {
		t.Fatalf("CountWedgedCompletedOverRetryLimit: %v", err)
	}
	if n != 1 {
		t.Errorf("over-limit count = %d, want 1", n)
	}
}

func TestAuthorRepo_GetByForeignIDNotFound(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)

	got, err := repo.GetByForeignID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("GetByForeignID missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing author, got %+v", got)
	}
}
