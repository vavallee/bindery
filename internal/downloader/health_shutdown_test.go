package downloader

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// TestRefreshDownloadClientHealthAsync_ShutDownGroupClearsTheChecking placeholder
// is the regression guard for #2372. The refresh writes a Checking placeholder
// before handing each probe to the jobs group. jobs.Group.Go is a documented
// no-op once the group has begun shutting down, so ignoring its return left the
// client showing "Checking download client paths" with nothing on its way to
// replace it.
func TestRefreshDownloadClientHealthAsync_ShutDownGroupClearsTheChecking(t *testing.T) {
	group := jobs.NewGroup(context.Background())
	if names := group.Shutdown(time.Second); len(names) != 0 {
		t.Fatalf("shutting down an idle group reported jobs still running: %v", names)
	}

	store := NewHealthStore()
	clients := []models.DownloadClient{
		{ID: 1, Name: "qb", Type: "qbittorrent", Host: "127.0.0.1", Port: 1, Enabled: true},
		{ID: 2, Name: "off", Type: "qbittorrent", Host: "127.0.0.1", Port: 2, Enabled: false},
	}

	RefreshDownloadClientHealthAsync(context.Background(), group, store, clients, "", "", "")

	if got := store.Get(1); got != nil {
		t.Errorf("enabled client kept a %q placeholder for a probe that will never run: %+v", got.Status, got)
	}
	if got := store.Get(2); got != nil {
		t.Errorf("disabled client should never have been touched, got %+v", got)
	}
}
