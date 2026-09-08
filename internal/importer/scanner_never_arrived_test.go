package importer

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #2505. A grab can be recorded as successful without the client ever
// receiving anything, and the poll then found the torrent missing on every
// cycle and did nothing: the queue item sat at downloading with an empty
// errorMessage forever, which on screen looks exactly like a torrent waiting
// on peers.
//
// qBittorrent holds no torrents at all here, so every tracked hash misses both
// the category map and the unfiltered fallback. The unfiltered listing
// succeeding is what makes sourceListIsComplete true.
func newNeverArrivedFixture(t *testing.T) (*Scanner, *db.DownloadRepo, *sql.DB, *models.DownloadClient, context.Context) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	s := NewScanner(dlRepo, clientRepo, db.NewBookRepo(database), db.NewAuthorRepo(database),
		db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "qbit-2505", Type: "qbittorrent", Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return s, dlRepo, database, client, ctx
}

// DownloadRepo.Create stamps added_at itself and does not persist GrabbedAt, so
// an old grab has to be back-dated in the row, which is also the shape a real
// one has: nothing on the grab path writes grabbed_at, so the age check falls
// back to added_at.
func mkNeverArrived(t *testing.T, ctx context.Context, dlRepo *db.DownloadRepo, database *sql.DB, client *models.DownloadClient, guid string, addedAt time.Time) int64 {
	t.Helper()
	hash := "dddddddddddddddddddddddddddddddddddddddd"
	dl := &models.Download{
		GUID:             guid,
		Title:            "a book the client never got",
		NZBURL:           "magnet:?xt=urn:btih:" + hash,
		Status:           models.StateDownloading,
		Protocol:         "torrent",
		TorrentID:        &hash,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET added_at=? WHERE id=?", addedAt.UTC(), dl.ID); err != nil {
		t.Fatalf("back-date download: %v", err)
	}
	return dl.ID
}

func TestNeverArrived_FailsADownloadTheClientNeverReceived(t *testing.T) {
	s, dlRepo, database, client, ctx := newNeverArrivedFixture(t)
	id := mkNeverArrived(t, ctx, dlRepo, database, client, "guid-2505-old", time.Now().Add(-2*neverArrivedGrace))

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StateFailed {
		t.Errorf("status = %q, want %q: a download the client never received stayed stuck", got.Status, models.StateFailed)
	}
	if got.ErrorMessage == "" {
		t.Error("errorMessage is empty, so the queue still shows no reason for the stall")
	}
}

// The grace period is the guard against failing a client that is merely slow
// to publish a torrent, which qBittorrent does while it resolves a magnet.
func TestNeverArrived_LeavesARecentGrabAlone(t *testing.T) {
	s, dlRepo, database, client, ctx := newNeverArrivedFixture(t)
	id := mkNeverArrived(t, ctx, dlRepo, database, client, "guid-2505-fresh", time.Now())

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByID(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StateDownloading {
		t.Errorf("status = %q, want %q: a grab inside the grace period was failed early", got.Status, models.StateDownloading)
	}
}
