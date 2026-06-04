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
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/models"
)

// qbitReconcileFixture wires up an in-memory DB, a book already present in the
// library (so the content-path-gone shortcut deterministically marks the
// download imported once the torrent is found), and a download client. The
// caller supplies the HTTP handler so each test controls the qBittorrent API
// shape (category filtering, torrent state, etc.).
type qbitReconcileFixture struct {
	scanner *Scanner
	client  *models.DownloadClient
	dlRepo  *db.DownloadRepo
	book    *models.Book
}

func newQbitReconcileFixture(t *testing.T, handler http.HandlerFunc, category, audioCategory string) *qbitReconcileFixture {
	t.Helper()

	libraryDir := t.TempDir()
	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	author := &models.Author{Name: "Recon Author", ForeignID: "a-recon", SortName: "Author, Recon"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Recon Book", ForeignID: "b-recon", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:              "qbit-recon",
		Type:              "qbittorrent",
		Host:              host,
		Port:              port,
		Enabled:           true,
		Category:          category,
		CategoryAudiobook: audioCategory,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	return &qbitReconcileFixture{scanner: s, client: client, dlRepo: dlRepo, book: book}
}

// TestCheckQbittorrentDownloads_CompleteAtGrab_CategoryMiss is the core #969
// regression test.
//
// Scenario: Bindery grabs a release whose torrent the client ALREADY holds
// complete + seeding (cross-seed). The AddTorrent 409 path recovers the hash but
// its setCategory call is best-effort, so the torrent stays under the
// cross-seed's ORIGINAL category ("crossseed") rather than Bindery's configured
// "ebook". The download record sits at StateDownloading with the correct hash.
//
// The mock returns the torrent ONLY for an unfiltered (no category) query and an
// empty list for category="ebook". With the old behaviour the category-filtered
// poll never sees the torrent and the download is wedged at downloading forever.
// The unfiltered-listing fallback must find it by hash and drive it to imported.
func TestCheckQbittorrentDownloads_CompleteAtGrab_CategoryMiss(t *testing.T) {
	const torrentHash = "cr0ssseed1234567890cr0ssseed1234567890ab"
	saveRoot := t.TempDir()

	fx := newQbitReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			cat := r.URL.Query().Get("category")
			// The torrent lives under a foreign category; only the unfiltered
			// (cat=="") query returns it. Bindery's "ebook" query is empty.
			if cat == "" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"hash":         torrentHash,
					"name":         "Recon Book",
					"state":        "stalledUP",
					"progress":     1.0,
					"amount_left":  0,
					"category":     "crossseed",
					"save_path":    saveRoot,
					"content_path": "", // files live at the cross-seed location → "already imported" shortcut
				}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "ebook", "")

	ctx := context.Background()
	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-crossseed",
		Title:            "Recon Book",
		Status:           models.StateDownloading,
		Protocol:         "torrent",
		TorrentID:        &hash,
		BookID:           &fx.book.ID,
		DownloadClientID: &fx.client.ID,
	}
	if err := fx.dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	fx.scanner.checkQbittorrentDownloads(ctx, fx.client)

	got, err := fx.dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("#969 regression: complete-at-grab cross-seed torrent under a foreign "+
			"category must be reconciled to imported via the unfiltered listing; got %q", got.Status)
	}
}

// TestCheckQbittorrentDownloads_NilHash_Backfill is the #939 regression test.
//
// Scenario: a download has its DownloadClientID set but TorrentID nil (the hash
// was never persisted because SendDownload failed to return a RemoteID, or the
// row predates the fix). The old poll skipped any nil-hash record forever, so
// the queue item stayed stuck.
//
// The reconciliation matches the torrent by name in the unfiltered listing,
// backfills the hash, and imports. The test asserts BOTH the hash was persisted
// and the download reached imported.
func TestCheckQbittorrentDownloads_NilHash_Backfill(t *testing.T) {
	const torrentHash = "nilhash01234567890nilhash01234567890abcd"
	saveRoot := t.TempDir()

	fx := newQbitReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":         torrentHash,
				"name":         "Recon Book",
				"state":        "stalledUP",
				"progress":     1.0,
				"amount_left":  0,
				"category":     "ebook",
				"save_path":    saveRoot,
				"content_path": "",
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "ebook", "")

	ctx := context.Background()
	dl := &models.Download{
		GUID:             "guid-nilhash",
		Title:            "Recon Book",
		Status:           models.StateDownloading,
		Protocol:         "torrent",
		TorrentID:        nil, // the bug: no hash was ever stored
		BookID:           &fx.book.ID,
		DownloadClientID: &fx.client.ID,
	}
	if err := fx.dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	fx.scanner.checkQbittorrentDownloads(ctx, fx.client)

	got, err := fx.dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != torrentHash {
		t.Errorf("#939 regression: nil-hash download must have its torrent hash backfilled "+
			"from the listing match; got TorrentID=%v want %q", got.TorrentID, torrentHash)
	}
	if got.Status != models.StateImported {
		t.Errorf("#939 regression: nil-hash download must be reconciled to imported, got %q", got.Status)
	}
}

// TestCheckQbittorrentDownloads_NormalDownloadingToComplete guards against a
// regression in the ordinary happy path: a torrent under Bindery's configured
// category that completes normally must still drive the download to imported.
func TestCheckQbittorrentDownloads_NormalDownloadingToComplete(t *testing.T) {
	const torrentHash = "normalpath1234567890normalpath1234567890"
	saveRoot := t.TempDir()

	fx := newQbitReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			// Present under the configured category, as a normal grab would be.
			cat := r.URL.Query().Get("category")
			if cat != "" && cat != "ebook" {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":         torrentHash,
				"name":         "Recon Book",
				"state":        "uploading",
				"progress":     1.0,
				"amount_left":  0,
				"category":     "ebook",
				"save_path":    saveRoot,
				"content_path": "",
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "ebook", "")

	ctx := context.Background()
	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-normal",
		Title:            "Recon Book",
		Status:           models.StateDownloading,
		Protocol:         "torrent",
		TorrentID:        &hash,
		BookID:           &fx.book.ID,
		DownloadClientID: &fx.client.ID,
	}
	if err := fx.dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	fx.scanner.checkQbittorrentDownloads(ctx, fx.client)

	got, err := fx.dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("normal downloading→complete path regressed: expected imported, got %q", got.Status)
	}
}

// TestCheckQbittorrentDownloads_NoDoubleImport asserts that an already-imported
// download (importedAt set, terminal StateImported) is left untouched even while
// the torrent is still seeding and reported complete — the importer must not
// re-run on a finished record (idempotency, ref #706).
func TestCheckQbittorrentDownloads_NoDoubleImport(t *testing.T) {
	const torrentHash = "alreadydone1234567890alreadydone12345678"
	saveRoot := t.TempDir()

	var importCalls int
	fx := newQbitReconcileFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":         torrentHash,
				"name":         "Recon Book",
				"state":        "stalledUP",
				"progress":     1.0,
				"amount_left":  0,
				"category":     "ebook",
				"save_path":    saveRoot,
				"content_path": "",
			}})
		case "/api/v2/torrents/files":
			// If the importer were (wrongly) re-entered for the imported record it
			// would ask for the file list here. Count the calls to prove it doesn't.
			importCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "ebook", "")

	ctx := context.Background()
	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-done",
		Title:            "Recon Book",
		Status:           models.StateImported,
		Protocol:         "torrent",
		TorrentID:        &hash,
		BookID:           &fx.book.ID,
		DownloadClientID: &fx.client.ID,
	}
	if err := fx.dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	fx.scanner.checkQbittorrentDownloads(ctx, fx.client)

	got, err := fx.dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("imported download must stay imported, got %q", got.Status)
	}
	if importCalls != 0 {
		t.Errorf("no-double-import: importer was re-entered for an already-imported download (%d file-list calls)", importCalls)
	}
}

// TestMatchTorrentForDownload_AmbiguousNameRefuses verifies the conservative
// nil-hash matcher: two torrents share the download's name and neither (or both)
// match the client category, so the matcher must refuse rather than backfill a
// guessed — possibly wrong — hash.
func TestMatchTorrentForDownload_AmbiguousNameRefuses(t *testing.T) {
	client := &models.DownloadClient{Category: "ebook"}
	dl := &models.Download{Title: "Dune"}
	candidates := []qbittorrent.Torrent{
		{Hash: "aaaa", Name: "Dune", Category: "movies"},
		{Hash: "bbbb", Name: "Dune", Category: "music"},
	}
	if _, ok := matchTorrentForDownload(client, dl, candidates); ok {
		t.Fatal("ambiguous name match must refuse, but a candidate was returned")
	}

	// One of them under the client's category → unambiguous, take it.
	candidates[1].Category = "ebook"
	got, ok := matchTorrentForDownload(client, dl, candidates)
	if !ok || got.Hash != "bbbb" {
		t.Fatalf("category tie-break failed: ok=%v hash=%q", ok, got.Hash)
	}
}
