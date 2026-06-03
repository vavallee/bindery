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
	"github.com/vavallee/bindery/internal/downloader/sabnzbd"
	"github.com/vavallee/bindery/internal/models"
)

// sabHistoryHandler returns an httptest handler that mimics the subset of the
// SABnzbd JSON API checkSABnzbdDownloads + tryImportSABnzbd touch:
//   - mode=history (GET history)  -> returns the supplied slots
//   - mode=history&name=delete    -> DeleteHistory cleanup; records the call
//
// SAB routes every request to /api and dispatches on the "mode" (and "name")
// query params, so the handler dispatches the same way.
func sabHistoryHandler(t *testing.T, slots []sabnzbd.HistorySlot, deleted *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("mode") {
		case "history":
			if q.Get("name") == "delete" {
				if deleted != nil {
					*deleted = append(*deleted, q.Get("value"))
				}
				_ = json.NewEncoder(w).Encode(sabnzbd.SimpleResponse{Status: true})
				return
			}
			_ = json.NewEncoder(w).Encode(sabnzbd.HistoryResponse{
				History: sabnzbd.HistoryData{Slots: slots},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func sabClient(t *testing.T, ctx context.Context, clientRepo *db.DownloadClientRepo, url string) *models.DownloadClient {
	t.Helper()
	host, port := scannerTestHostPort(t, url)
	client := &models.DownloadClient{
		Name:    "sab",
		Type:    "sabnzbd",
		Host:    host,
		Port:    port,
		APIKey:  "testkey",
		Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

// TestCheckSABnzbdDownloads_CompletedAlreadyImported is the SAB analogue of
// TestCheckQbittorrentDownloads_ContentPathGoneAlreadyImported. A completed
// SABnzbd job points at an empty/missing download dir (files were already
// moved into the library by a prior import), the book is tracked in book_files
// with a real on-disk file, so the scanner must mark the download StateImported
// via the already-in-library shortcut rather than failing with "no book files".
func TestCheckSABnzbdDownloads_CompletedAlreadyImported(t *testing.T) {
	libraryDir := t.TempDir()
	emptyDownloadDir := t.TempDir() // no book files inside

	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const nzoID = "SABnzbd_nzo_done"
	var deleted []string
	srv := httptest.NewServer(sabHistoryHandler(t, []sabnzbd.HistorySlot{{
		NzoID:  nzoID,
		Name:   "My Book",
		Status: "Completed",
		Path:   emptyDownloadDir,
	}}, &deleted))
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

	author := &models.Author{Name: "Test Author", ForeignID: "a-sab1", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-sab1", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	client := sabClient(t, ctx, clientRepo, srv.URL)

	nzo := nzoID
	dl := &models.Download{
		GUID:             "guid-sab-done",
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

	s.checkSABnzbdDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("expected status %q (book already in library), got %q", models.StateImported, got.Status)
	}
}

// TestCheckSABnzbdDownloads_Failed verifies the "Failed" branch transitions the
// download to StateFailed via setDownloadError. SAB analogue of the failed
// download scenario.
func TestCheckSABnzbdDownloads_Failed(t *testing.T) {
	const nzoID = "SABnzbd_nzo_fail"
	srv := httptest.NewServer(sabHistoryHandler(t, []sabnzbd.HistorySlot{{
		NzoID:       nzoID,
		Name:        "Broken Book",
		Status:      "Failed",
		FailMessage: "unpack failed: missing par2",
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

	client := sabClient(t, ctx, clientRepo, srv.URL)

	nzo := nzoID
	dl := &models.Download{
		GUID:             "guid-sab-fail",
		Title:            "Broken Book",
		Status:           models.StateDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkSABnzbdDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateFailed {
		t.Errorf("expected status %q after SAB reports Failed, got %q", models.StateFailed, got.Status)
	}
}

// TestCheckSABnzbdDownloads_RetriesImportFailed is the SAB analogue of
// TestCheckQbittorrentDownloads_RetriesImportFailed (Bug #7). A completed SAB
// job whose Download row is stuck in StateImportFailed must be retried on each
// cycle, incrementing ImportRetryCount, and stop once the cap is reached.
//
// The import is forced to keep failing by pointing the job at a download dir
// with no book files AND no already-in-library record, so each retry takes the
// "no book files found" failure path and the counter advances deterministically.
func TestCheckSABnzbdDownloads_RetriesImportFailed(t *testing.T) {
	emptyDownloadDir := t.TempDir() // never contains a book file

	const nzoID = "SABnzbd_nzo_retry"
	srv := httptest.NewServer(sabHistoryHandler(t, []sabnzbd.HistorySlot{{
		NzoID:  nzoID,
		Name:   "Retry Book",
		Status: "Completed",
		Path:   emptyDownloadDir,
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

	client := sabClient(t, ctx, clientRepo, srv.URL)

	nzo := nzoID
	dl := &models.Download{
		GUID:             "guid-sab-retry",
		Title:            "Retry Book",
		Status:           models.StateImportFailed,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	// First cycle: retry must fire and increment the counter to 1.
	s.checkSABnzbdDownloads(ctx, client)
	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get after first retry: %v", err)
	}
	if got.ImportRetryCount != 1 {
		t.Errorf("Bug #7 regression: expected ImportRetryCount=1 after first cycle, got %d", got.ImportRetryCount)
	}

	// Drive up to the cap.
	for got.ImportRetryCount < importRetryLimit {
		s.checkSABnzbdDownloads(ctx, client)
		got, err = dlRepo.GetByGUID(ctx, dl.GUID)
		if err != nil {
			t.Fatalf("get download: %v", err)
		}
	}
	if got.ImportRetryCount != importRetryLimit {
		t.Fatalf("expected ImportRetryCount=%d at cap, got %d", importRetryLimit, got.ImportRetryCount)
	}

	// One more cycle: counter must not exceed the cap.
	s.checkSABnzbdDownloads(ctx, client)
	got, err = dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get after cap: %v", err)
	}
	if got.ImportRetryCount > importRetryLimit {
		t.Errorf("Bug #7 regression: ImportRetryCount exceeded cap %d (got %d)", importRetryLimit, got.ImportRetryCount)
	}
}

// TestCheckSABnzbdDownloads_SourceGoneNoPanic covers the stale-source path: the
// download row has no matching slot in SAB history (entry cleared / aged out).
// The poll must complete without panicking and leave the row untouched (it is
// not retry-exhausted, so blockStaleImportFailures must not terminally fail it).
func TestCheckSABnzbdDownloads_SourceGoneNoPanic(t *testing.T) {
	// History reports an unrelated NZO, so GetByNzoID never matches our row.
	srv := httptest.NewServer(sabHistoryHandler(t, []sabnzbd.HistorySlot{{
		NzoID:  "SABnzbd_nzo_other",
		Name:   "Someone Else",
		Status: "Completed",
		Path:   t.TempDir(),
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

	client := sabClient(t, ctx, clientRepo, srv.URL)

	nzo := "SABnzbd_nzo_gone"
	dl := &models.Download{
		GUID:             "guid-sab-gone",
		Title:            "Vanished Book",
		Status:           models.StateDownloading,
		Protocol:         "usenet",
		SABnzbdNzoID:     &nzo,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkSABnzbdDownloads(ctx, client) // must not panic

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	// Not retry-exhausted and not StateImportFailed: blockStaleImportFailures
	// leaves a healthy StateDownloading row alone (SAB history is paginated, so
	// a missing slot is not a definitive "gone" signal).
	if got.Status != models.StateDownloading {
		t.Errorf("expected StateDownloading to be preserved for a stale-but-healthy row, got %q", got.Status)
	}
}

// TestTryImportSABnzbd_AlreadyImported exercises tryImportSABnzbd directly
// (rather than via the poll loop): an empty download dir + an already-tracked
// audiobook on disk must yield StateImported, mirroring
// TestTryImportInternal_EmptyPathAlreadyImported but through the SAB wrapper.
func TestTryImportSABnzbd_AlreadyImported(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	emptyDownloadDir := t.TempDir()

	libM4b := filepath.Join(audiobookDir, "book.m4b")
	if err := os.WriteFile(libM4b, []byte("m4b-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const nzoID = "SABnzbd_nzo_direct"
	var deleted []string
	srv := httptest.NewServer(sabHistoryHandler(t, nil, &deleted))
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

	author := &models.Author{Name: "Test Author", ForeignID: "a-sab-direct", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-sab-direct", Status: "wanted", MediaType: models.MediaTypeAudiobook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, libM4b); err != nil {
		t.Fatal(err)
	}

	client := sabClient(t, ctx, clientRepo, srv.URL)
	sab := sabnzbd.New(client.Host, client.Port, client.APIKey, client.URLBase, client.UseSSL)

	dl := &models.Download{
		GUID:   "guid-sab-direct",
		Title:  "My Book",
		Status: models.StateCompleted,
		BookID: &book.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportSABnzbd(ctx, sab, dl, nzoID, emptyDownloadDir)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("expected status %q (book already in library), got %q", models.StateImported, got.Status)
	}
}
