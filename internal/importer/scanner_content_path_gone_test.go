package importer

// Test for the row that qBittorrent reports complete while no content path for
// it exists on this host (#2616).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestCheckQbittorrentDownloads_ContentPathGoneMarksImportFailed covers the row
// whose torrent the client reports complete while no content path is on this
// host. It used to log "will retry next cycle" and leave the status alone, so
// the row sat in grabbed forever: the retry guard only counts importFailed
// rows, so nothing ever spent a retry on it and nothing ever moved it.
//
// The expected end state is the one an import failure reaches — importFailed
// with a reason naming the missing files — from which the existing skip streak
// blocks the row if they never appear.
func TestCheckQbittorrentDownloads_ContentPathGoneMarksImportFailed(t *testing.T) {
	// An existing save path that does not contain the torrent name, and no
	// content_path: this is the shape resolveQbitContentPath rejects.
	savePath := t.TempDir()

	const torrentHash = "9999aaaa8888bbbb7777cccc6666dddd5555eeee"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "My Book",
				"state":        "stalledUP",
				"progress":     1.0,
				"save_path":    savePath,
				"content_path": "",
			}}
			_ = json.NewEncoder(w).Encode(torrents)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit-content-gone",
		Type:    "qbittorrent",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-content-gone",
		Title:            "My Book",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.StateImportFailed {
		t.Fatalf("status = %q, want %q: a complete download with no content path on this host must leave the in-progress states", got.Status, models.StateImportFailed)
	}
	if !strings.Contains(got.ErrorMessage, "no content path") {
		t.Errorf("error message must name the missing content path, got %q", got.ErrorMessage)
	}
}
