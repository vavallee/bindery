package db

import (
	"context"
	"testing"

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
