package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate126KeepsExistingDownloadsUnstamped: a download that predates the
// media_type column has no recorded slot, and the migration must not invent
// one. The row survives intact and reads back with an empty MediaType, which
// is what sends the importer down its old fallback chain (#1964).
func TestMigrate126KeepsExistingDownloadsUnstamped(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	mustExec(t, database, `ALTER TABLE downloads DROP COLUMN media_type`)
	mustExec(t, database, `INSERT INTO downloads (guid, title, nzb_url, size, status, protocol, quality)
		VALUES ('legacy-1', 'Andy Weir - Project Hail Mary (Unabridged) M4B', 'http://x/1', 10, 'downloading', 'usenet', 'm4b')`)

	v126 := migrationVersionForTest(t, "126_download_media_type.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v126); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 126: %v", err)
	}

	repo := NewDownloadRepo(database)
	got, err := repo.GetByGUID(ctx, "legacy-1")
	if err != nil || got == nil {
		t.Fatalf("legacy row lost: %v %v", got, err)
	}
	if got.MediaType != "" || got.Quality != "m4b" || got.Status != models.StateDownloading {
		t.Errorf("legacy row = MediaType %q Quality %q Status %q, want empty / m4b / downloading", got.MediaType, got.Quality, got.Status)
	}
}

// TestDownloadRepoMediaTypeRoundTrip: Create stores the stamp and every read
// returns it, and a re-grab that claims a dead row replaces it with the new
// grab's slot instead of carrying the old one over.
func TestDownloadRepoMediaTypeRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)

	dl := &models.Download{GUID: "g1", Title: "t", NZBURL: "u", Status: models.StateGrabbed,
		Protocol: "usenet", MediaType: models.MediaTypeAudiobook}
	if err := repo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, dl.ID)
	if err != nil || got == nil || got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("stamp not stored: %+v %v", got, err)
	}

	if err := repo.UpdateStatus(ctx, dl.ID, models.StateFailed); err != nil {
		t.Fatal(err)
	}
	regrab := &models.Download{ID: dl.ID, GUID: "g1", Title: "t", NZBURL: "u",
		Protocol: "usenet", MediaType: models.MediaTypeEbook}
	ok, err := repo.RetryFailed(ctx, regrab)
	if err != nil || !ok {
		t.Fatalf("RetryFailed = %v, %v", ok, err)
	}
	got, _ = repo.GetByID(ctx, dl.ID)
	if got == nil || got.MediaType != models.MediaTypeEbook {
		t.Errorf("re-grab kept the old stamp: %+v", got)
	}

	// A claim that carries no stamp clears the old one rather than keeping it.
	if err := repo.UpdateStatus(ctx, dl.ID, models.StateFailed); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.RetryFailed(ctx, &models.Download{ID: dl.ID, GUID: "g1", Title: "t", NZBURL: "u", Protocol: "usenet"}); err != nil || !ok {
		t.Fatalf("second RetryFailed = %v, %v", ok, err)
	}
	got, _ = repo.GetByID(ctx, dl.ID)
	if got == nil || got.MediaType != "" {
		t.Errorf("unstamped re-grab left stamp %q", got.MediaType)
	}
}
