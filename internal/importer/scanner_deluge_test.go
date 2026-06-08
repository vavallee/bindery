package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// delugeStub is a minimal Deluge Web JSON-RPC server returning a single
// seeding torrent and its file list. It mirrors the shape the deluge client
// expects (result/error/id over POST /json).
func delugeStub(t *testing.T, hash, savePath string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int64             `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		write := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
		}
		switch req.Method {
		case "auth.login", "auth.check_session":
			write(true)
		case "core.get_torrents_status":
			write(map[string]any{
				hash: map[string]any{
					"name": "testbook", "hash": hash,
					"progress": 100.0, "state": "Seeding",
					"total_size": 12, "total_done": 12,
					"download_location": savePath,
				},
			})
		case "core.get_torrent_status":
			write(map[string]any{"files": []map[string]any{{"path": "testbook.epub", "size": 12}}})
		default:
			write(nil)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCheckDelugeDownloads_ImportsOnSeeding is the regression test for #1019:
// a Deluge torrent that has reached seeding must be detected as complete and
// imported. Before the fix Deluge had no poller (it fell through to the SABnzbd
// path) so the download stayed at "downloading" forever.
func TestCheckDelugeDownloads_ImportsOnSeeding(t *testing.T) {
	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "testbook.epub"), []byte("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	libraryDir := t.TempDir()
	const hash = "deadbeef1234567890deadbeef1234567890dead"

	srv := delugeStub(t, hash, downloadDir)

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
	settings := db.NewSettingsRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")
	s.WithSettings(settings)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "OLA", Name: "Test Author", SortName: "Author, Test", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OLB", AuthorID: author.ID, Title: "Test Book", SortTitle: "test book", Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{Name: "deluge", Type: "deluge", Host: host, Port: port, Enabled: true}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	h := hash
	dl := &models.Download{
		GUID: "guid-deluge", Title: "testbook", NZBURL: "magnet:?xt=urn:btih:" + hash,
		Status: models.StateDownloading, Protocol: "torrent",
		TorrentID: &h, DownloadClientID: &client.ID, BookID: &book.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkDelugeDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-deluge")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("download status = %q, want %q (seeding torrent should import)", got.Status, models.StateImported)
	}
	// The epub must have landed under the library dir.
	var found bool
	_ = filepath.Walk(libraryDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(p) == ".epub" {
			found = true
		}
		return nil
	})
	if !found {
		t.Errorf("no .epub imported under library dir %s", libraryDir)
	}
}
