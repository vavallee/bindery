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
		Size: 2048, Status: models.DownloadStatusQueued, Protocol: "torrent",
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

	// Set and look up.
	if err := repo.SetTorrentID(ctx, dl.ID, "hash123"); err != nil {
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
