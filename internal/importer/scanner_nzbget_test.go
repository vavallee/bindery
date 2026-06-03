package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader/nzbget"
	"github.com/vavallee/bindery/internal/models"
)

// nzbgetHandler returns an httptest handler that mimics the subset of NZBGet's
// JSON-RPC API checkNZBGetDownloads + tryImportNZBGet touch. NZBGet POSTs every
// call to /jsonrpc and dispatches on the "method" field of the request body:
//   - "history"   -> returns the supplied items (params [false])
//   - "editqueue" -> RemoveHistory cleanup; returns result:true
//
// removed, when non-nil, records each editqueue call so cleanup can be asserted.
func nzbgetHandler(t *testing.T, items []nzbget.HistoryItem, removed *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		switch req.Method {
		case "history":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": items})
		case "editqueue":
			if removed != nil {
				*removed++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func nzbgetClient(t *testing.T, ctx context.Context, clientRepo *db.DownloadClientRepo, url string) *models.DownloadClient {
	t.Helper()
	host, port := scannerTestHostPort(t, url)
	client := &models.DownloadClient{
		Name:    "nzbget",
		Type:    "nzbget",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

// TestCheckNZBGetDownloads_CompletedAlreadyImported is the NZBGet analogue of
// TestCheckSABnzbdDownloads_CompletedAlreadyImported. A SUCCESS history item
// points at an empty DestDir (files already in the library); the book is tracked
// in book_files, so the scanner must mark the download StateImported via the
// already-in-library shortcut.
func TestCheckNZBGetDownloads_CompletedAlreadyImported(t *testing.T) {
	libraryDir := t.TempDir()
	emptyDestDir := t.TempDir() // no book files

	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const nzbID = 4242
	var removed int
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID:   nzbID,
		NZBName: "My Book",
		Status:  "SUCCESS/ALL",
		DestDir: emptyDestDir,
	}}, &removed))
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	author := &models.Author{Name: "Test Author", ForeignID: "a-ng1", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-ng1", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	client := nzbgetClient(t, ctx, clientRepo, srv.URL)

	// NZBGet matches by NZBID stored as a string in sabnzbd_nzo_id.
	nzo := "4242"
	dl := &models.Download{
		GUID:             "guid-ng-done",
		Title:            "My Book",
		Status:           models.StateGrabbed,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		BookID:           &book.ID,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkNZBGetDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("expected status %q (book already in library), got %q", models.StateImported, got.Status)
	}
}

// TestCheckNZBGetDownloads_Failed verifies the IsFailure branch transitions the
// download to StateFailed. NZBGet reports failure with a "FAILURE/..." status.
func TestCheckNZBGetDownloads_Failed(t *testing.T) {
	const nzbID = 99
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID:   nzbID,
		NZBName: "Broken Book",
		Status:  "FAILURE/UNPACK",
	}}, nil))
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

	client := nzbgetClient(t, ctx, clientRepo, srv.URL)

	nzo := "99"
	dl := &models.Download{
		GUID:             "guid-ng-fail",
		Title:            "Broken Book",
		Status:           models.StateDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkNZBGetDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateFailed {
		t.Errorf("expected status %q after NZBGet reports FAILURE, got %q", models.StateFailed, got.Status)
	}
}

// TestCheckNZBGetDownloads_RetriesImportFailed is the NZBGet analogue of
// Bug #7's retry test. A SUCCESS item whose Download row is StateImportFailed
// must be retried each cycle (incrementing ImportRetryCount) and stop at the cap.
// The empty DestDir + no library record forces each import to fail, advancing
// the counter deterministically.
func TestCheckNZBGetDownloads_RetriesImportFailed(t *testing.T) {
	emptyDestDir := t.TempDir()

	const nzbID = 7
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID:   nzbID,
		NZBName: "Retry Book",
		Status:  "SUCCESS/ALL",
		DestDir: emptyDestDir,
	}}, nil))
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

	client := nzbgetClient(t, ctx, clientRepo, srv.URL)

	nzo := "7"
	dl := &models.Download{
		GUID:             "guid-ng-retry",
		Title:            "Retry Book",
		Status:           models.StateImportFailed,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkNZBGetDownloads(ctx, client)
	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get after first retry: %v", err)
	}
	if got.ImportRetryCount != 1 {
		t.Errorf("Bug #7 regression: expected ImportRetryCount=1 after first cycle, got %d", got.ImportRetryCount)
	}

	for got.ImportRetryCount < importRetryLimit {
		s.checkNZBGetDownloads(ctx, client)
		got, err = dlRepo.GetByGUID(ctx, dl.GUID)
		if err != nil {
			t.Fatalf("get download: %v", err)
		}
	}
	if got.ImportRetryCount != importRetryLimit {
		t.Fatalf("expected ImportRetryCount=%d at cap, got %d", importRetryLimit, got.ImportRetryCount)
	}

	s.checkNZBGetDownloads(ctx, client)
	got, err = dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get after cap: %v", err)
	}
	if got.ImportRetryCount > importRetryLimit {
		t.Errorf("Bug #7 regression: ImportRetryCount exceeded cap %d (got %d)", importRetryLimit, got.ImportRetryCount)
	}
}

// TestCheckNZBGetDownloads_SourceGoneNoPanic covers the stale-source path: the
// download row has no matching item in NZBGet history. The poll must complete
// without panicking and leave a healthy StateDownloading row untouched.
func TestCheckNZBGetDownloads_SourceGoneNoPanic(t *testing.T) {
	srv := httptest.NewServer(nzbgetHandler(t, []nzbget.HistoryItem{{
		NZBID:   1111,
		NZBName: "Someone Else",
		Status:  "SUCCESS/ALL",
		DestDir: t.TempDir(),
	}}, nil))
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

	client := nzbgetClient(t, ctx, clientRepo, srv.URL)

	nzo := "2222" // not present in history
	dl := &models.Download{
		GUID:             "guid-ng-gone",
		Title:            "Vanished Book",
		Status:           models.StateDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkNZBGetDownloads(ctx, client) // must not panic

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateDownloading {
		t.Errorf("expected StateDownloading to be preserved for a stale-but-healthy row, got %q", got.Status)
	}
}

// TestTryImportNZBGet_AlreadyImported exercises tryImportNZBGet directly: an
// empty download dir + an already-tracked audiobook on disk must yield
// StateImported, mirroring the SAB direct-wrapper test.
func TestTryImportNZBGet_AlreadyImported(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	emptyDownloadDir := t.TempDir()

	libM4b := filepath.Join(audiobookDir, "book.m4b")
	if err := os.WriteFile(libM4b, []byte("m4b-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const nzbID = 555
	var removed int
	srv := httptest.NewServer(nzbgetHandler(t, nil, &removed))
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

	author := &models.Author{Name: "Test Author", ForeignID: "a-ng-direct", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-ng-direct", Status: "wanted", MediaType: models.MediaTypeAudiobook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, libM4b); err != nil {
		t.Fatal(err)
	}

	client := nzbgetClient(t, ctx, clientRepo, srv.URL)
	ng := nzbget.New(client.Host, client.Port, client.Username, client.Password, client.URLBase, client.UseSSL)

	dl := &models.Download{
		GUID:   "guid-ng-direct",
		Title:  "My Book",
		Status: models.StateCompleted,
		BookID: &book.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportNZBGet(ctx, ng, dl, nzbID, emptyDownloadDir)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("expected status %q (book already in library), got %q", models.StateImported, got.Status)
	}
}
