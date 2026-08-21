package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

// TestRefreshDownloadClientHealth_ProbesEveryEnabledClient covers the periodic
// re-probe added for #2029. Health used to be written only at boot and on
// create/update/test, so a client that broke afterwards stayed green until
// somebody pressed Test on a hunch.
func TestRefreshDownloadClientHealth_ProbesEveryEnabledClient(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()
	ctx := context.Background()

	clients := db.NewDownloadClientRepo(database)
	// Two enabled clients of types that used to be skipped entirely, plus a
	// disabled one that must stay skipped.
	for _, c := range []*models.DownloadClient{
		{Name: "sab", Type: "sabnzbd", Host: "127.0.0.1", Port: 1, Enabled: true},
		{Name: "nzbget", Type: "nzbget", Host: "127.0.0.1", Port: 1, Enabled: true},
		{Name: "off", Type: "deluge", Host: "127.0.0.1", Port: 1, Enabled: false},
	} {
		if err := clients.Create(ctx, c); err != nil {
			t.Fatalf("create client %s: %v", c.Name, err)
		}
	}

	store := downloader.NewHealthStore()
	s := &Scheduler{clients: clients}
	s.WithStoragePaths("/downloads", "")
	s.WithDownloadClientHealth(store, "")

	s.refreshDownloadClientHealth(ctx)

	all, err := clients.List(ctx)
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}
	for _, c := range all {
		got := store.Get(c.ID)
		if c.Enabled && got == nil {
			t.Errorf("enabled client %q was never probed", c.Name)
		}
		if !c.Enabled && got != nil {
			t.Errorf("disabled client %q was probed", c.Name)
		}
	}
}

// TestRefreshDownloadClientHealth_InertWithoutWiring: a scheduler with no health
// store must do nothing rather than panic, which is what every test and caller
// that has not been wired up relies on.
func TestRefreshDownloadClientHealth_InertWithoutWiring(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()

	// No store attached.
	s := &Scheduler{clients: db.NewDownloadClientRepo(database)}
	s.refreshDownloadClientHealth(context.Background())

	// No client repo either.
	s2 := &Scheduler{}
	s2.WithDownloadClientHealth(downloader.NewHealthStore(), "")
	s2.refreshDownloadClientHealth(context.Background())
}
