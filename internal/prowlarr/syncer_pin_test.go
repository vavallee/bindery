package prowlarr

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestSyncer_PreservesDownloadClientPin runs a sync against the real indexer
// repo. The pin is local configuration, like priority, and Prowlarr knows
// nothing about it, so an update driven by a Prowlarr side rename and a new
// seed ratio must write the row back with the pin intact (#3080).
func TestSyncer_PreservesDownloadClientPin(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	instances := db.NewProwlarrRepo(database)
	inst := &models.ProwlarrInstance{Name: "p", URL: "http://prowlarr.invalid", APIKey: "k", Enabled: true}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	clients := db.NewDownloadClientRepo(database)
	priv := &models.DownloadClient{Name: "qbit-private", Type: "qbittorrent", Host: "qb.local", Port: 8081, Enabled: true}
	if err := clients.Create(ctx, priv); err != nil {
		t.Fatal(err)
	}
	indexers := db.NewIndexerRepo(database)

	first := prowlarrStub(t, `[{"id":7,"name":"Tracker","enable":true,"protocol":"torrent","supportsSearch":true,
		"categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedRatio","value":1.0}]}]`)
	defer first.Close()
	if _, err := NewSyncer(New(first.URL, "k"), indexers, instanceTouch{}).Sync(ctx, inst.ID); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	rows, err := indexers.ListByProwlarrInstance(ctx, inst.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("after first sync: %v (%d rows)", err, len(rows))
	}
	row := rows[0]
	row.DownloadClientID = &priv.ID
	if err := indexers.Update(ctx, &row); err != nil {
		t.Fatalf("pin: %v", err)
	}

	second := prowlarrStub(t, `[{"id":7,"name":"Tracker Renamed","enable":true,"protocol":"torrent","supportsSearch":true,
		"categories":[{"id":7020}],"fields":[{"name":"torrentBaseSettings.seedRatio","value":2.5}]}]`)
	defer second.Close()
	res, err := NewSyncer(New(second.URL, "k"), indexers, instanceTouch{}).Sync(ctx, inst.ID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("the second sync should have updated the row, got %s", res)
	}
	rows, err = indexers.ListByProwlarrInstance(ctx, inst.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("after second sync: %v (%d rows)", err, len(rows))
	}
	if rows[0].Name != "Tracker Renamed" {
		t.Fatalf("the sync did not apply the rename, so this test proves nothing: %q", rows[0].Name)
	}
	if rows[0].DownloadClientID == nil || *rows[0].DownloadClientID != priv.ID {
		t.Errorf("the sync dropped the download client pin: %v", rows[0].DownloadClientID)
	}
}

type instanceTouch struct{}

func (instanceTouch) SetLastSyncAt(context.Context, int64, time.Time) error { return nil }
