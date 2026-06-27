// Package integration holds end-to-end tests that exercise more than one
// internal package wired together against fakes (in-memory SQLite, an
// httptest download client) rather than a single unit in isolation.
//
// pipeline_test.go is the missing end-to-end coverage of the core
// grab -> import -> history pipeline. The per-package unit tests each cover a
// slice (the api package tests Grab against a fake SABnzbd; the importer
// package tests tryImportInternal's effects), but nothing exercises a single
// download row flowing from a real grab handler through the real importer and
// asserts the cumulative persisted state. Wiring regressions — a grab that
// forgets to associate the book, an import that writes book_files but never
// flips status, a history event that is dropped — hide exactly in that seam.
//
// Both halves share ONE :memory: database and operate on the SAME download +
// book rows, so this is a genuinely contiguous slice rather than two unrelated
// fixtures stitched together.
package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// writeMinimalEpub writes a tiny but structurally-valid EPUB (zip + OPF) so the
// importer's format detection and embedded-metadata reads operate on real
// bytes rather than a placeholder. Mirrors importer.writeEpubAt, duplicated
// here because that helper is unexported in the importer test package.
func writeMinimalEpub(t *testing.T, path, title, author, isbn string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	add("content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>`+title+`</dc:title>
    <dc:creator opf:role="aut">`+author+`</dc:creator>
    <dc:identifier>urn:isbn:`+isbn+`</dc:identifier>
  </metadata>
</package>`)
	if err := zw.Close(); err != nil {
		t.Fatalf("close epub zip: %v", err)
	}
}

func hostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port from %q: %v", raw, err)
	}
	return u.Hostname(), port
}

func hasEvent(events []models.HistoryEvent, eventType string) *models.HistoryEvent {
	for i := range events {
		if events[i].EventType == eventType {
			return &events[i]
		}
	}
	return nil
}

// TestPipeline_GrabImportHistory drives a single download row through the REAL
// grab handler (api.QueueHandler.Grab against a fake SABnzbd) and then the REAL
// importer (importer.Scanner.ImportFromPath), and asserts the cumulative
// persisted effects of the whole chain on one shared :memory: database.
//
// Pipeline stages exercised, in order:
//
//	GRAB    api.QueueHandler.Grab -> downloader.SendDownload -> fake SABnzbd
//	        -> persists a Download row (StateGrabbed -> StateDownloading) bound
//	        to the seeded book, and writes a HistoryEventGrabbed row.
//	IMPORT  importer.Scanner.ImportFromPath -> tryImportInternal -> places the
//	        epub under the library root, AddBookFile writes a book_files row and
//	        (via refreshBookStatus) flips the book to BookStatusImported, and a
//	        HistoryEventBookImported row is written; the download flips to
//	        StateImported.
//
// The four assertions below are the integration value — each pins a distinct
// hand-off in the wiring:
//
//  1. file placed under the library root        (importer file-placement wiring)
//  2. book_files row + book status == imported  (importer -> db status wiring)
//  3. download status == imported               (importer terminal-state wiring)
//  4. history timeline grabbed THEN bookImported (grab + import history wiring)
func TestPipeline_GrabImportHistory(t *testing.T) {
	// The grab calls downloader.SendDownload against a loopback httptest
	// server; the SSRF guard blocks loopback by default, so open it for this
	// test the same way the api package's own grab tests do.
	defer httpsec.AllowLoopbackForTests()()

	ctx := context.Background()

	// One shared in-memory database for BOTH halves of the pipeline.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	downloadRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	historyRepo := db.NewHistoryRepo(database)
	settingsRepo := db.NewSettingsRepo(database)

	// Pin import mode to "copy" so the source survives and the destination is
	// deterministic (the default would be "hardlink" since both temp dirs are
	// on the same device, and would couple the assertion to FS behaviour).
	if err := settingsRepo.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatalf("set import.mode: %v", err)
	}

	// --- Seed: an author + a single WANTED ebook. ---
	author := &models.Author{
		ForeignID: "OLA-PIPE", Name: "Pipeline Author", SortName: "Author, Pipeline",
		Monitored: true, MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{
		ForeignID: "OLB-PIPE", AuthorID: author.ID,
		Title: "Pipeline Book", SortTitle: "pipeline book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	// --- Fakes: an indexer NZB endpoint + a SABnzbd "addfile" endpoint. ---
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
	}))
	defer indexerSrv.Close()

	var sabAddCalls int
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "addfile" {
			t.Errorf("SABnzbd: expected mode=addfile, got %q", r.URL.Query().Get("mode"))
		}
		sabAddCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  true,
			"nzo_ids": []string{"nzo-pipeline"},
		})
	}))
	defer sabSrv.Close()

	host, port := hostPort(t, sabSrv.URL)
	client := &models.DownloadClient{
		Name: "sab", Type: "sabnzbd", Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatalf("create download client: %v", err)
	}

	// ================= STAGE 1: GRAB (real handler) =================
	queue := api.NewQueueHandler(downloadRepo, clientRepo, bookRepo, historyRepo)

	grabBody, _ := json.Marshal(map[string]any{
		"guid":      "pipeline-guid",
		"nzbUrl":    indexerSrv.URL + "/release.nzb",
		"title":     "Pipeline Book",
		"size":      4242,
		"bookId":    book.ID,
		"protocol":  "usenet",
		"mediaType": models.MediaTypeEbook,
	})
	rec := httptest.NewRecorder()
	queue.Grab(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(grabBody)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grab: expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if sabAddCalls != 1 {
		t.Fatalf("grab: expected exactly one SABnzbd addfile call, got %d", sabAddCalls)
	}

	// The grab must have persisted a download row bound to the seeded book.
	dl, err := downloadRepo.GetByGUID(ctx, "pipeline-guid")
	if err != nil || dl == nil {
		t.Fatalf("reload grabbed download: %v", err)
	}
	if dl.BookID == nil || *dl.BookID != book.ID {
		t.Fatalf("grab did not bind download to book: got BookID=%v want %d", dl.BookID, book.ID)
	}
	if dl.Status != models.StateDownloading {
		t.Fatalf("grab: download status = %q, want %q", dl.Status, models.StateDownloading)
	}

	// ================= STAGE 2: IMPORT (real importer) =================
	// The download has completed on the client; simulate the completed payload
	// landing in the download dir, then run the real importer against it.
	libraryDir := t.TempDir()
	downloadDir := t.TempDir()
	writeMinimalEpub(t,
		filepath.Join(downloadDir, "Pipeline Author - Pipeline Book.epub"),
		"Pipeline Book", "Pipeline Author", "9780000000001")

	// Advance the persisted download to "completed" exactly as the download-
	// client poller does when SABnzbd reports the job finished. This goes
	// through the REAL state machine (downloading -> completed); the importer's
	// own internal completed -> importing -> imported transitions are validated
	// against this persisted state, so skipping it would (correctly) be
	// rejected as an invalid transition.
	if err := downloadRepo.UpdateStatus(ctx, dl.ID, models.StateCompleted); err != nil {
		t.Fatalf("advance download to completed: %v", err)
	}
	dl.Status = models.StateCompleted

	scanner := importer.NewScanner(
		downloadRepo, clientRepo, bookRepo, authorRepo, historyRepo,
		libraryDir, "", "", "", "",
	).WithSettings(settingsRepo)

	scanner.ImportFromPath(ctx, dl, downloadDir, "")

	// ----- Assertion 1: the file was placed under the library root. -----
	var placed string
	if err := filepath.Walk(libraryDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(p) == ".epub" {
			placed = p
		}
		return nil
	}); err != nil {
		t.Fatalf("walk library dir: %v", err)
	}
	if placed == "" {
		t.Fatalf("import: no .epub landed under library root %s", libraryDir)
	}

	// ----- Assertion 2: a book_files row exists and the book flipped to imported. -----
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("list book files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("import: want exactly 1 book_files row, got %d (%+v)", len(files), files)
	}
	if files[0].Path != placed {
		t.Errorf("import: book_files path = %q, want placed file %q", files[0].Path, placed)
	}
	reloadedBook, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil || reloadedBook == nil {
		t.Fatalf("reload book: %v", err)
	}
	if reloadedBook.Status != models.BookStatusImported {
		t.Fatalf("import: book status = %q, want %q — book_files written but status not flipped",
			reloadedBook.Status, models.BookStatusImported)
	}

	// ----- Assertion 3: the download flipped to the terminal imported state. -----
	reloadedDownload, err := downloadRepo.GetByGUID(ctx, "pipeline-guid")
	if err != nil || reloadedDownload == nil {
		t.Fatalf("reload download after import: %v", err)
	}
	if reloadedDownload.Status != models.StateImported {
		t.Fatalf("import: download status = %q, want %q", reloadedDownload.Status, models.StateImported)
	}

	// ----- Assertion 4: the history timeline is grabbed THEN bookImported. -----
	events, err := historyRepo.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatalf("list history for book: %v", err)
	}
	grabbed := hasEvent(events, models.HistoryEventGrabbed)
	if grabbed == nil {
		t.Fatalf("history: missing %q event for book — grab did not record history; got %+v",
			models.HistoryEventGrabbed, events)
	}
	imported := hasEvent(events, models.HistoryEventBookImported)
	if imported == nil {
		t.Fatalf("history: missing %q event for book — import did not record history; got %+v",
			models.HistoryEventBookImported, events)
	}
	// bookImported must come strictly after grabbed in wall-clock terms.
	if !imported.CreatedAt.After(grabbed.CreatedAt) && !imported.CreatedAt.Equal(grabbed.CreatedAt) {
		t.Errorf("history: bookImported (%s) recorded before grabbed (%s)",
			imported.CreatedAt, grabbed.CreatedAt)
	}
	// The bookImported event must carry the format so the queue can show ebook
	// vs audiobook (regression seam for the media_type='both' case).
	var data map[string]string
	if err := json.Unmarshal([]byte(imported.Data), &data); err != nil {
		t.Fatalf("unmarshal bookImported data %q: %v", imported.Data, err)
	}
	if data["format"] != models.MediaTypeEbook {
		t.Errorf("history: bookImported format = %q, want %q", data["format"], models.MediaTypeEbook)
	}
}
