package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

// A stall row for a download with no book belongs to the download's owner, so
// it stays in that user's history alone under tenancy.
func TestStallHistoryOwnedByDownloadOwner(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	bob, err := db.NewUserRepo(database).Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	downloads := db.NewDownloadRepo(database)
	dl := &models.Download{GUID: "bob-stall", Title: "Bob Unbound Release", Status: models.StateDownloading, Protocol: "torrent", OwnerUserID: bob.ID}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	history := db.NewHistoryRepo(database)
	s := &Scheduler{
		downloads: downloads,
		books:     db.NewBookRepo(database),
		settings:  db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database),
		history:   history,
	}
	s.handleStalledDownload(ctx, dl, nil, downloader.StallClientReported)

	evs, err := history.ListByType(ctx, models.HistoryEventDownloadStalled)
	if err != nil || len(evs) != 1 {
		t.Fatalf("stall history = %v, %v; want one row", evs, err)
	}
	if evs[0].OwnerUserID != bob.ID {
		t.Errorf("stall history owner = %d, want bob (%d)", evs[0].OwnerUserID, bob.ID)
	}
}
