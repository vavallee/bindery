package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate136 runs migration 136 on a database that looks like one from
// before it (v1.52.0: the column absent, live and finished downloads present)
// and checks the upgrade can never make an old row read as never received:
// rows that were live are marked seen, finished rows are left alone. Then the
// Down section is run and the rows survive it.
func TestMigrate136UpgradeAndDown(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	content, err := migrationsFS.ReadFile("migrations/136_downloads_client_seen_at.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s, "-- +migrate Down")
	if idx < 0 {
		t.Fatal("migration 136 has no Down section")
	}
	down := func() {
		for _, stmt := range splitSQLStatements(s[idx+len("-- +migrate Down"):]) {
			mustExec(t, database, stmt)
		}
	}
	columnCount := func() int {
		var n int
		if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name = 'client_seen_at'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Down on the fresh schema removes the column.
	down()
	if columnCount() != 0 {
		t.Fatal("Down left downloads.client_seen_at in place")
	}

	// A pre-136 database: live and finished rows, no column.
	repo := NewDownloadRepo(database)
	for guid, status := range map[string]models.DownloadState{
		"g-downloading": models.StateDownloading,
		"g-grabbed":     models.StateGrabbed,
		"g-failed":      models.StateFailed,
		"g-imported":    models.StateImported,
	} {
		d := &models.Download{GUID: guid, Title: guid, Status: status, Protocol: "torrent"}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	// Upgrade: forget 136 and run the migrator.
	mustExec(t, database, `DELETE FROM schema_migrations WHERE version = ?`,
		migrationVersionForTest(t, "136_downloads_client_seen_at.sql"))
	if err := migrate(database); err != nil {
		t.Fatalf("migrate 136: %v", err)
	}
	if columnCount() != 1 {
		t.Fatal("Up did not add downloads.client_seen_at")
	}
	for guid, want := range map[string]bool{
		"g-downloading": true, // predates the evidence, must not read as never received
		"g-grabbed":     true,
		"g-failed":      false,
		"g-imported":    false,
	} {
		d, err := repo.GetByGUID(ctx, guid)
		if err != nil || d == nil {
			t.Fatalf("%s: %v", guid, err)
		}
		seen, err := repo.ClientSeen(ctx, d.ID)
		if err != nil || seen != want {
			t.Errorf("%s: seen = %v (%v), want %v", guid, seen, err, want)
		}
	}

	// Down again keeps every row.
	down()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM downloads`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows after Down = %d (%v), want 4", n, err)
	}
}

// The flag is written on the first sighting only.
func TestMarkClientSeenWritesOnce(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)
	d := &models.Download{GUID: "g-once", Title: "t", Status: models.StateDownloading, Protocol: "torrent"}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if seen, _ := repo.ClientSeen(ctx, d.ID); seen {
		t.Fatal("a new row reads as seen")
	}
	first, err := repo.MarkClientSeen(ctx, d.ID)
	if err != nil || !first {
		t.Fatalf("first sighting = %v (%v), want true", first, err)
	}
	var stamp1 time.Time
	if err := database.QueryRow(`SELECT client_seen_at FROM downloads WHERE id = ?`, d.ID).Scan(&stamp1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	again, err := repo.MarkClientSeen(ctx, d.ID)
	if err != nil || again {
		t.Fatalf("second sighting = %v (%v), want false: no write", again, err)
	}
	var stamp2 time.Time
	if err := database.QueryRow(`SELECT client_seen_at FROM downloads WHERE id = ?`, d.ID).Scan(&stamp2); err != nil {
		t.Fatal(err)
	}
	if !stamp1.Equal(stamp2) {
		t.Fatalf("the stamp moved from %v to %v on a later poll", stamp1, stamp2)
	}
}

// A re-grab is a new attempt: the sighting of the old one must not carry over.
func TestRegrabClearsClientSeen(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewDownloadRepo(database)
	d := &models.Download{GUID: "g-regrab", Title: "t", Status: models.StateDownloading, Protocol: "torrent"}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkClientSeen(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetError(ctx, d.ID, "removed"); err != nil {
		t.Fatal(err)
	}
	ok, err := repo.RetryDeadForAutoGrab(ctx, d, time.Now().Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("regrab claim = %v (%v)", ok, err)
	}
	if seen, _ := repo.ClientSeen(ctx, d.ID); seen {
		t.Fatal("a re-grabbed row still reads as seen by the client")
	}
}
