package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// A history row the scanner writes for a download with no book belongs to the
// download's owner, so it stays in their history alone under tenancy.
func TestScannerHistoryOwnedByDownloadOwner(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	bob, err := db.NewUserRepo(database).Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	dlRepo := db.NewDownloadRepo(database)
	dl := &models.Download{GUID: "bob-guid", Title: "Bob Unbound Grab", Status: models.StateDownloading, OwnerUserID: bob.ID}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	history := db.NewHistoryRepo(database)
	s := &Scanner{downloads: dlRepo, history: history}
	s.markDownloadFailed(ctx, dl, "torrent errored")

	evs, err := history.List(ctx)
	if err != nil || len(evs) != 1 {
		t.Fatalf("history = %v, %v; want one row", evs, err)
	}
	if evs[0].OwnerUserID != bob.ID {
		t.Errorf("history owner = %d, want bob (%d)", evs[0].OwnerUserID, bob.ID)
	}
}
