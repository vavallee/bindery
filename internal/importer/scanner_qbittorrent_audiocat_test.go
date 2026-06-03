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

// TestCheckQbittorrentDownloads_PollsAudiobookCategory is the regression test
// for the audiobook-category poll gap.
//
// Scenario: a client is configured with Category="ebook" and
// CategoryAudiobook="audiobooks". An audiobook was grabbed under the
// "audiobooks" category (as the grab path does via ResolveCategory). The import
// poll previously queried only Category ("ebook"), so qBittorrent never returned
// the audiobook torrent, the download's hash never matched any torrent, and it
// hung in its current state forever ("download not found in torrent list").
//
// The mock server returns the torrent ONLY when queried with category
// "audiobooks" and an empty list for "ebook". Reaching StateImported therefore
// proves the poll queried the audiobook category — with the old single-category
// behaviour the torrent is invisible and the download stays StateGrabbed.
func TestCheckQbittorrentDownloads_PollsAudiobookCategory(t *testing.T) {
	saveRoot := t.TempDir()
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()

	// The audiobook is already on disk in the library (placed by a prior import),
	// so the content-path-gone shortcut deterministically marks it imported once
	// the torrent is actually found.
	libM4b := filepath.Join(audiobookDir, "book.m4b")
	if err := os.WriteFile(libM4b, []byte("m4b-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const torrentHash = "audi0b00k1234567890audi0b00k1234567890ab"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Honour the category filter the way qBittorrent does: the torrent
			// lives under "audiobooks" and must be absent from any other query.
			if r.URL.Query().Get("category") != "audiobooks" {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "My Audiobook",
				"state":        "missingFiles",
				"progress":     1.0,
				"save_path":    saveRoot,
				"content_path": "", // files were moved to the library on a prior import
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, audiobookDir, "", "", "")

	author := &models.Author{Name: "Audio Author", ForeignID: "a-audiocat", SortName: "Author, Audio"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Audiobook", ForeignID: "b-audiocat", Status: "wanted", MediaType: models.MediaTypeAudiobook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, libM4b); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:              "qbit-audiocat",
		Type:              "qbittorrent",
		Host:              host,
		Port:              port,
		Enabled:           true,
		Category:          "ebook",
		CategoryAudiobook: "audiobooks",
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-audiocat",
		Title:            "My Audiobook",
		Status:           models.StateGrabbed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		BookID:           &book.ID,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("audiobook-category regression: expected status %q (torrent found under the audiobook "+
			"category and recognised as already imported), got %q — the poll did not query CategoryAudiobook",
			models.StateImported, got.Status)
	}
}
