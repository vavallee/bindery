package db

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// readRemoveOnImport reads one client back through every getter that scans
// remove_on_import and fails the test if any of them disagrees with want.
// Each getter has its own scan, so a column added to one and missed in
// another would only show up on the path that missed it.
func readRemoveOnImport(t *testing.T, repo *DownloadClientRepo, id int64, want bool) {
	t.Helper()
	ctx := context.Background()

	got, err := repo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetByID(%d): %v, %v", id, got, err)
	}
	if got.RemoveOnImport != want {
		t.Errorf("GetByID: RemoveOnImport = %v, want %v", got.RemoveOnImport, want)
	}

	findIn := func(name string, list []models.DownloadClient, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range list {
			if c.ID == id {
				if c.RemoveOnImport != want {
					t.Errorf("%s: RemoveOnImport = %v, want %v", name, c.RemoveOnImport, want)
				}
				return
			}
		}
		t.Fatalf("%s: client %d not listed", name, id)
	}
	list, err := repo.List(ctx)
	findIn("List", list, err)
	enabled, err := repo.ListEnabled(ctx)
	findIn("ListEnabled", enabled, err)
	byProto, err := repo.GetEnabledByProtocol(ctx, "torrent")
	findIn("GetEnabledByProtocol", byProto, err)
}

// TestDownloadClientRepo_RemoveOnImportRoundTrip covers the #2046 column
// through Create, Update and every read path, in both directions, so a true
// survives a save and a false is not read back as true.
func TestDownloadClientRepo_RemoveOnImportRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	on := &models.DownloadClient{
		Name: "qbit on", Type: "qbittorrent", Host: "qbit.local", Port: 8080,
		Enabled: true, Priority: 1, RemoveOnImport: true,
	}
	off := &models.DownloadClient{
		Name: "transmission off", Type: "transmission", Host: "trans.local", Port: 9091,
		Enabled: true, Priority: 2, RemoveOnImport: false,
	}
	for _, c := range []*models.DownloadClient{on, off} {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("create %s: %v", c.Name, err)
		}
	}
	readRemoveOnImport(t, repo, on.ID, true)
	readRemoveOnImport(t, repo, off.ID, false)

	// Flip both and save: the toggle must be writable in each direction.
	on.RemoveOnImport = false
	off.RemoveOnImport = true
	for _, c := range []*models.DownloadClient{on, off} {
		if err := repo.Update(ctx, c); err != nil {
			t.Fatalf("update %s: %v", c.Name, err)
		}
	}
	readRemoveOnImport(t, repo, on.ID, false)
	readRemoveOnImport(t, repo, off.ID, true)
}

// TestDownloadClientRepo_RemoveOnImportDefaultsOffForPre090Rows rebuilds the
// state before migration 090: a download_clients table with no
// remove_on_import column and a client already saved in it. Re-running the
// migration must add the column with the toggle OFF for that client, so an
// upgrade never starts removing torrents nobody asked to have removed.
func TestDownloadClientRepo_RemoveOnImportDefaultsOffForPre090Rows(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	if _, err := database.ExecContext(ctx, `ALTER TABLE download_clients DROP COLUMN remove_on_import`); err != nil {
		t.Fatalf("drop remove_on_import to reproduce the pre-090 schema: %v", err)
	}
	now := time.Now().UTC()
	res, err := database.ExecContext(ctx, `
		INSERT INTO download_clients (name, type, host, port, api_key, use_ssl, url_base, username, password,
			category, priority, enabled, created_at, updated_at)
		VALUES ('pre-090 qbit', 'qbittorrent', 'qbit.local', 8080, '', 0, '', 'admin', 'pw', 'books', 0, 1, ?, ?)`,
		now, now)
	if err != nil {
		t.Fatalf("insert pre-090 client: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	v090 := migrationVersionForTest(t, "090_download_client_remove_on_import.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v090); err != nil {
		t.Fatalf("clear migration 090 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 090: %v", err)
	}

	readRemoveOnImport(t, NewDownloadClientRepo(database), id, false)
}

// TestDownloadClientRepo_UnreadableRemoveOnImportIsAnError guards the scan of
// the column in the read paths. SQLite stores whatever it is handed, so a
// hand edited row can hold text in remove_on_import; the read must fail
// loudly on it rather than settle the toggle either way and carry on.
func TestDownloadClientRepo_UnreadableRemoveOnImportIsAnError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	repo := NewDownloadClientRepo(database)

	c := &models.DownloadClient{Name: "qbit", Type: "qbittorrent", Host: "qbit.local", Port: 8080, Enabled: true}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE download_clients SET remove_on_import = 'yes please' WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.List(ctx); err == nil {
		t.Error("List: expected a scan error for a non-integer remove_on_import")
	}
	if _, err := repo.ListEnabled(ctx); err == nil {
		t.Error("ListEnabled: expected a scan error for a non-integer remove_on_import")
	}
	if _, err := repo.GetEnabledByProtocol(ctx, "torrent"); err == nil {
		t.Error("GetEnabledByProtocol: expected a scan error for a non-integer remove_on_import")
	}
	if _, err := repo.GetByID(ctx, c.ID); err == nil {
		t.Error("GetByID: expected a scan error for a non-integer remove_on_import")
	}
}
