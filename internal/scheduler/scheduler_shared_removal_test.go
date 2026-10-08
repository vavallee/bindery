package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// TestCheckStalledDownloads_KeepsTorrentAnotherDownloadUses: two rows name
// one torrent (the client adopted it for the second grab). Only the older row
// is past the stall timeout. Its stall must not remove the torrent, and its
// data, out from under the newer row, which is still within its own window.
// When the newer row stalls later, it is the last user and removes it.
func TestCheckStalledDownloads_KeepsTorrentAnotherDownloadUses(t *testing.T) {
	const hash = "deadbeef"
	deletes := &stallDeleteRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": "DEADBEEF", "state": "stalledDL"}})
		case "/api/v2/torrents/delete":
			deletes.record(r)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	host, port := stallServerHostPort(t, srv.URL)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer database.Close()
	ctx := context.Background()

	clientsRepo := db.NewDownloadClientRepo(database)
	client := &models.DownloadClient{Name: "qb", Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p", Enabled: true}
	if err := clientsRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	downloadsRepo := db.NewDownloadRepo(database)
	mk := func(guid string, grabbedAt time.Time) *models.Download {
		tid := hash
		dl := &models.Download{
			GUID: guid, Title: "Shared Release", DownloadClientID: &client.ID,
			Status: models.StateGrabbed, Protocol: "torrent", TorrentID: &tid,
		}
		if err := downloadsRepo.Create(ctx, dl); err != nil {
			t.Fatalf("create download: %v", err)
		}
		if _, err := database.ExecContext(ctx, "UPDATE downloads SET status=?, grabbed_at=? WHERE id=?",
			models.StateDownloading, grabbedAt, dl.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		return dl
	}
	older := mk("g-older", time.Now().UTC().Add(-3*time.Hour))
	newer := mk("g-newer", time.Now().UTC())

	settingsRepo := db.NewSettingsRepo(database)
	_ = settingsRepo.Set(ctx, "autoGrab.enabled", "false")
	s := &Scheduler{
		downloads: downloadsRepo,
		clients:   clientsRepo,
		indexers:  db.NewIndexerRepo(database),
		books:     db.NewBookRepo(database),
		authors:   db.NewAuthorRepo(database),
		settings:  settingsRepo,
		blocklist: db.NewBlocklistRepo(database),
		history:   db.NewHistoryRepo(database),
		searcher:  indexer.NewSearcher(),
	}

	s.checkStalledDownloads(ctx)
	if hashes, _ := deletes.calls(); len(hashes) != 0 {
		t.Fatalf("the torrent was removed while the newer download still uses it: %v", hashes)
	}
	gotOlder, _ := downloadsRepo.GetByID(ctx, older.ID)
	if gotOlder == nil || gotOlder.Status != models.StateFailed {
		t.Fatalf("the stalled row should still be marked failed, got %+v", gotOlder)
	}
	gotNewer, _ := downloadsRepo.GetByID(ctx, newer.ID)
	if gotNewer == nil || gotNewer.Status != models.StateDownloading {
		t.Fatalf("the newer row must be untouched, got %+v", gotNewer)
	}

	// The newer row ages past the timeout. The older row is failed now and no
	// longer holds the torrent, so this stall removes it, data included.
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET grabbed_at=? WHERE id=?",
		time.Now().UTC().Add(-3*time.Hour), newer.ID); err != nil {
		t.Fatalf("backdate newer: %v", err)
	}
	s.checkStalledDownloads(ctx)
	hashes, files := deletes.calls()
	if len(hashes) != 1 || hashes[0] != hash || files[0] != "true" {
		t.Fatalf("expected one delete of %s with its data once no other row uses it, got %v %v", hash, hashes, files)
	}
}
