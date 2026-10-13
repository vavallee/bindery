package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/models"
)

// These tests cover "remove torrent after import" (#2046) end to end against
// fake qBittorrent and Transmission daemons. The contract they pin:
//
//   - the client is asked to remove the torrent only once the download row is
//     imported and the book is in the library;
//   - the client is never asked to delete the payload (qBittorrent
//     deleteFiles=false, Transmission without delete-local-data);
//   - nothing is asked of the client when the toggle is off, when the import
//     failed, or when the stored id is a legacy Transmission number;
//   - a removal the client refuses is logged and does not unwind the import.

const removeOnImportHash = "2046204620462046204620462046204620462046"

// removeOnImportFixture is a Scanner over an in-memory DB holding one wanted
// ebook. The download directory holds that book's file; the import mode is
// left at its default (auto), which hardlinks or copies, so the source stays
// where the client is seeding it.
type removeOnImportFixture struct {
	s           *Scanner
	books       *db.BookRepo
	book        *models.Book
	libraryDir  string
	downloadDir string
	sourceFile  string
	ctx         context.Context
}

func newRemoveOnImportFixture(t *testing.T) *removeOnImportFixture {
	t.Helper()
	libraryDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libraryDir)

	author := &models.Author{ForeignID: "OL-2046-author", Name: "Author A", SortName: "A, Author"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-2046-book",
		AuthorID:  author.ID,
		Title:     "Title T",
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeEbook,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	downloadDir := t.TempDir()
	source := filepath.Join(downloadDir, "book.epub")
	if err := os.WriteFile(source, bookSized("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &removeOnImportFixture{
		s: s, books: books, book: book,
		libraryDir: libraryDir, downloadDir: downloadDir, sourceFile: source, ctx: ctx,
	}
}

// markAlreadyInLibrary records a library file for the book, which is what the
// "book already in library" shortcuts look for.
func (fx *removeOnImportFixture) markAlreadyInLibrary(t *testing.T) {
	t.Helper()
	libEpub := filepath.Join(fx.libraryDir, "already.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fx.books.AddBookFile(fx.ctx, fx.book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}
}

func (fx *removeOnImportFixture) createClient(t *testing.T, srv *httptest.Server, typ string, removeOnImport bool) *models.DownloadClient {
	t.Helper()
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: typ + "-2046", Type: typ, Host: host, Port: port,
		Enabled: true, RemoveOnImport: removeOnImport,
	}
	if err := fx.s.clients.Create(fx.ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

func (fx *removeOnImportFixture) createDownload(t *testing.T, client *models.DownloadClient, status models.DownloadState, torrentID string) *models.Download {
	t.Helper()
	id := torrentID
	dl := &models.Download{
		GUID:             "guid-2046-" + client.Type,
		Title:            fx.book.Title,
		NZBURL:           "magnet:?xt=urn:btih:" + removeOnImportHash,
		Status:           status,
		Protocol:         "torrent",
		TorrentID:        &id,
		BookID:           &fx.book.ID,
		DownloadClientID: &client.ID,
	}
	if err := fx.s.downloads.Create(fx.ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	return dl
}

func (fx *removeOnImportFixture) status(t *testing.T, guid string) *models.Download {
	t.Helper()
	got, err := fx.s.downloads.GetByGUID(context.Background(), guid)
	if err != nil || got == nil {
		t.Fatalf("get download %s: %v, %v", guid, got, err)
	}
	return got
}

// libraryEbooks lists the .epub files under the library, skipping the one
// markAlreadyInLibrary planted.
func (fx *removeOnImportFixture) libraryEbooks(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(fx.libraryDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".epub") && d.Name() != "already.epub" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// removalState is what the fake daemon saw of the download at the moment a
// removal request arrived.
type removalState struct {
	status       models.DownloadState
	libraryFiles int
}

// snapshotAtRemoval returns the hook the fakes call when a removal arrives.
// It reads the download row and the library from the handler goroutine, so
// it records whether the import had already committed when the client was
// asked to drop the torrent.
func (fx *removeOnImportFixture) snapshotAtRemoval(t *testing.T, guid string, sink *[]removalState, mu *sync.Mutex) func() {
	return func() {
		got, err := fx.s.downloads.GetByGUID(context.Background(), guid)
		st := removalState{libraryFiles: len(fx.libraryEbooks(t))}
		if err == nil && got != nil {
			st.status = got.Status
		}
		mu.Lock()
		*sink = append(*sink, st)
		mu.Unlock()
	}
}

func captureRemoveOnImportLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func cleanupFailedLines(log string) string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "cleanup failed") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- qBittorrent

// fakeQbit serves the slice of the qBittorrent Web API the poller touches and
// records every /torrents/delete it receives.
type fakeQbit struct {
	t            *testing.T
	torrent      map[string]any
	files        []map[string]any // nil answers /torrents/files with 404
	deleteStatus int              // 0 means 200
	onDelete     func()

	mu      sync.Mutex
	deletes []url.Values
}

func (f *fakeQbit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v2/auth/login":
		_, _ = w.Write([]byte("Ok."))
	case "/api/v2/torrents/info":
		_ = json.NewEncoder(w).Encode([]map[string]any{f.torrent})
	case "/api/v2/torrents/files":
		if f.files == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(f.files)
	case "/api/v2/torrents/delete":
		if err := r.ParseForm(); err != nil {
			f.t.Errorf("parse delete form: %v", err)
		}
		f.mu.Lock()
		f.deletes = append(f.deletes, r.PostForm)
		f.mu.Unlock()
		if f.onDelete != nil {
			f.onDelete()
		}
		if f.deleteStatus != 0 {
			w.WriteHeader(f.deleteStatus)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeQbit) deleteRequests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.deletes...)
}

// qbitScenario is one way checkQbittorrentDownloads can close out a download.
type qbitScenario struct {
	name string
	// setup returns the torrent the fake lists, the files it reports and the
	// status the download row starts in.
	setup func(t *testing.T, fx *removeOnImportFixture) (torrent map[string]any, files []map[string]any, status models.DownloadState)
	// wantLibraryCopy is true when the import itself places a file (the
	// normal path); the shortcuts only close the row out.
	wantLibraryCopy bool
}

var qbitRemoveOnImportScenarios = []qbitScenario{
	{
		name: "normal import",
		setup: func(t *testing.T, fx *removeOnImportFixture) (map[string]any, []map[string]any, models.DownloadState) {
			return map[string]any{
					"hash": removeOnImportHash, "name": "book.epub", "state": "uploading",
					"progress": 1.0, "amount_left": 0, "save_path": fx.downloadDir,
					"content_path": fx.sourceFile,
				},
				[]map[string]any{{"index": 0, "name": "book.epub", "size": 12, "progress": 1}},
				models.StateDownloading
		},
		wantLibraryCopy: true,
	},
	{
		// content_path gone, book already in library, row still in flight
		// (scanner_poll.go, first "already in library" shortcut).
		name: "already in library shortcut from downloading",
		setup: func(t *testing.T, fx *removeOnImportFixture) (map[string]any, []map[string]any, models.DownloadState) {
			fx.markAlreadyInLibrary(t)
			return map[string]any{
				"hash": removeOnImportHash, "name": "Gone Release", "state": "missingFiles",
				"progress": 1.0, "save_path": t.TempDir(), "content_path": "",
			}, nil, models.StateDownloading
		},
	},
	{
		// Same, reached from the importFailed retry branch (second shortcut).
		name: "already in library shortcut from importFailed",
		setup: func(t *testing.T, fx *removeOnImportFixture) (map[string]any, []map[string]any, models.DownloadState) {
			fx.markAlreadyInLibrary(t)
			return map[string]any{
				"hash": removeOnImportHash, "name": "Gone Release", "state": "missingFiles",
				"progress": 1.0, "save_path": t.TempDir(), "content_path": "",
			}, nil, models.StateImportFailed
		},
	},
}

// TestCheckQbittorrentDownloads_RemoveOnImport covers the toggle ON on every
// path that marks a qBittorrent download imported: exactly one delete for the
// torrent's hash, deleteFiles=false, sent after the row is imported.
func TestCheckQbittorrentDownloads_RemoveOnImport(t *testing.T) {
	for _, sc := range qbitRemoveOnImportScenarios {
		t.Run(sc.name, func(t *testing.T) {
			fx := newRemoveOnImportFixture(t)
			torrent, files, status := sc.setup(t, fx)

			var mu sync.Mutex
			var seen []removalState
			fake := &fakeQbit{t: t, torrent: torrent, files: files}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			client := fx.createClient(t, srv, "qbittorrent", true)
			dl := fx.createDownload(t, client, status, removeOnImportHash)
			fake.onDelete = fx.snapshotAtRemoval(t, dl.GUID, &seen, &mu)

			fx.s.checkQbittorrentDownloads(fx.ctx, client)

			if got := fx.status(t, dl.GUID); got.Status != models.StateImported {
				t.Fatalf("download status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}

			reqs := fake.deleteRequests()
			if len(reqs) != 1 {
				t.Fatalf("delete requests = %d, want exactly 1: %v", len(reqs), reqs)
			}
			if got := reqs[0].Get("hashes"); got != removeOnImportHash {
				t.Errorf("delete hashes = %q, want %q", got, removeOnImportHash)
			}
			if got := reqs[0].Get("deleteFiles"); got != "false" {
				t.Errorf("deleteFiles = %q, want \"false\": Bindery must never ask the client to delete the payload", got)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0].status != models.StateImported {
				t.Errorf("at the moment of removal the download was %+v, want status imported", seen)
			}
			if sc.wantLibraryCopy {
				if seen[0].libraryFiles != 1 {
					t.Errorf("at the moment of removal the library held %d imported file(s), want 1", seen[0].libraryFiles)
				}
				if n := len(fx.libraryEbooks(t)); n != 1 {
					t.Errorf("library holds %d imported file(s), want 1", n)
				}
				// Default import mode is auto: hardlink or copy, never move,
				// so the file the client is seeding is untouched.
				if _, err := os.Stat(fx.sourceFile); err != nil {
					t.Errorf("source file gone after import with remove on import: %v", err)
				}
			}
		})
	}
}

// TestCheckQbittorrentDownloads_RemoveOnImportOff runs the same paths with
// the toggle off: the import still lands and the client is never asked to
// delete anything.
func TestCheckQbittorrentDownloads_RemoveOnImportOff(t *testing.T) {
	for _, sc := range qbitRemoveOnImportScenarios {
		t.Run(sc.name, func(t *testing.T) {
			fx := newRemoveOnImportFixture(t)
			torrent, files, status := sc.setup(t, fx)
			fake := &fakeQbit{t: t, torrent: torrent, files: files}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			client := fx.createClient(t, srv, "qbittorrent", false)
			dl := fx.createDownload(t, client, status, removeOnImportHash)

			fx.s.checkQbittorrentDownloads(fx.ctx, client)

			if got := fx.status(t, dl.GUID); got.Status != models.StateImported {
				t.Fatalf("download status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			if reqs := fake.deleteRequests(); len(reqs) != 0 {
				t.Errorf("toggle off but the client got %d delete request(s): %v", len(reqs), reqs)
			}
		})
	}
}

// TestCheckQbittorrentDownloads_RemoveOnImportFailureKeepsImport has the
// client answer the delete with HTTP 500. The import has already committed,
// so it must stay imported, the book stays in the library, and the failure
// is logged.
func TestCheckQbittorrentDownloads_RemoveOnImportFailureKeepsImport(t *testing.T) {
	for _, sc := range qbitRemoveOnImportScenarios {
		t.Run(sc.name, func(t *testing.T) {
			fx := newRemoveOnImportFixture(t)
			torrent, files, status := sc.setup(t, fx)
			fake := &fakeQbit{t: t, torrent: torrent, files: files, deleteStatus: http.StatusInternalServerError}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			client := fx.createClient(t, srv, "qbittorrent", true)
			dl := fx.createDownload(t, client, status, removeOnImportHash)
			logBuf := captureRemoveOnImportLog(t)

			fx.s.checkQbittorrentDownloads(fx.ctx, client)

			got := fx.status(t, dl.GUID)
			if got.Status != models.StateImported {
				t.Fatalf("a refused removal unwound the import: status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			if got.ErrorMessage != "" {
				t.Errorf("a refused removal wrote an error onto the download: %q", got.ErrorMessage)
			}
			if len(fake.deleteRequests()) != 1 {
				t.Errorf("delete requests = %d, want 1", len(fake.deleteRequests()))
			}
			if sc.wantLibraryCopy && len(fx.libraryEbooks(t)) != 1 {
				t.Errorf("imported file missing from the library after a refused removal")
			}
			lines := cleanupFailedLines(logBuf.String())
			if !strings.Contains(lines, "remove torrent "+removeOnImportHash) || !strings.Contains(lines, "HTTP 500") {
				t.Errorf("expected a cleanup failed warning naming the torrent and the HTTP 500, got:\n%s", lines)
			}
		})
	}
}

// TestCheckQbittorrentDownloads_RemoveOnImportSkippedWhenImportFails covers
// imports that do not land: the torrent holds no book file, so the import
// fails, and the client must not be asked to drop the torrent it may need
// for a retry.
func TestCheckQbittorrentDownloads_RemoveOnImportSkippedWhenImportFails(t *testing.T) {
	fx := newRemoveOnImportFixture(t)
	// Replace the book with a file the importer does not take.
	if err := os.Remove(fx.sourceFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.downloadDir, "release.nfo"), []byte("no book here"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeQbit{t: t,
		torrent: map[string]any{
			"hash": removeOnImportHash, "name": filepath.Base(fx.downloadDir), "state": "uploading",
			"progress": 1.0, "amount_left": 0, "save_path": filepath.Dir(fx.downloadDir),
			"content_path": fx.downloadDir,
		},
		files: []map[string]any{{"index": 0, "name": filepath.Base(fx.downloadDir) + "/release.nfo", "size": 12, "progress": 1}},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := fx.createClient(t, srv, "qbittorrent", true)
	dl := fx.createDownload(t, client, models.StateDownloading, removeOnImportHash)

	fx.s.checkQbittorrentDownloads(fx.ctx, client)

	if got := fx.status(t, dl.GUID); got.Status != models.StateImportFailed {
		t.Fatalf("status = %q (%s), want importFailed for an import with no book file", got.Status, got.ErrorMessage)
	}
	if reqs := fake.deleteRequests(); len(reqs) != 0 {
		t.Errorf("import failed but the client got %d delete request(s): %v", len(reqs), reqs)
	}
}

// --------------------------------------------------------------- Transmission

// fakeTransmission answers torrent-get with a fixed listing and records every
// torrent-remove call's arguments.
type fakeTransmission struct {
	t            *testing.T
	torrents     []map[string]any
	removeResult string // "" means "success"
	onRemove     func()

	mu      sync.Mutex
	removes []map[string]any
}

func (f *fakeTransmission) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/transmission/rpc" {
		f.t.Errorf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var req struct {
		Method    string         `json:"method"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode rpc body: %v", err)
	}
	switch req.Method {
	case "torrent-remove":
		f.mu.Lock()
		f.removes = append(f.removes, req.Arguments)
		f.mu.Unlock()
		if f.onRemove != nil {
			f.onRemove()
		}
		result := f.removeResult
		if result == "" {
			result = "success"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "arguments": map[string]any{}})
	case "torrent-get":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    "success",
			"arguments": map[string]any{"torrents": f.torrents},
		})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{}})
	}
}

func (f *fakeTransmission) removeRequests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.removes...)
}

func (fx *removeOnImportFixture) transmissionTorrent() map[string]any {
	return map[string]any{
		"id": 7, "hashString": removeOnImportHash, "name": "book.epub",
		"status": 6, "percentDone": 1.0, "downloadDir": fx.downloadDir,
		"files": []map[string]any{{"name": "book.epub", "length": 12, "bytesCompleted": 12}},
	}
}

// TestCheckTransmissionDownloads_RemoveOnImport: toggle ON sends one
// torrent-remove naming the info hash, with no delete-local-data, after the
// import committed. Toggle OFF sends none.
func TestCheckTransmissionDownloads_RemoveOnImport(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "toggle off"
		if on {
			name = "toggle on"
		}
		t.Run(name, func(t *testing.T) {
			fx := newRemoveOnImportFixture(t)
			fake := &fakeTransmission{t: t, torrents: []map[string]any{fx.transmissionTorrent()}}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			client := fx.createClient(t, srv, "transmission", on)
			dl := fx.createDownload(t, client, models.StateDownloading, removeOnImportHash)
			var mu sync.Mutex
			var seen []removalState
			fake.onRemove = fx.snapshotAtRemoval(t, dl.GUID, &seen, &mu)

			fx.s.checkTransmissionDownloads(fx.ctx, client)

			if got := fx.status(t, dl.GUID); got.Status != models.StateImported {
				t.Fatalf("download status = %q (%s), want imported", got.Status, got.ErrorMessage)
			}
			if n := len(fx.libraryEbooks(t)); n != 1 {
				t.Errorf("library holds %d imported file(s), want 1", n)
			}
			if _, err := os.Stat(fx.sourceFile); err != nil {
				t.Errorf("source file gone after import: %v", err)
			}

			reqs := fake.removeRequests()
			if !on {
				if len(reqs) != 0 {
					t.Errorf("toggle off but the daemon got %d torrent-remove call(s): %v", len(reqs), reqs)
				}
				return
			}
			if len(reqs) != 1 {
				t.Fatalf("torrent-remove calls = %d, want exactly 1: %v", len(reqs), reqs)
			}
			ids, _ := reqs[0]["ids"].([]any)
			if len(ids) != 1 || ids[0] != removeOnImportHash {
				t.Errorf("torrent-remove ids = %v, want [%q] (the info hash, never the session id)", reqs[0]["ids"], removeOnImportHash)
			}
			// The client only sends delete-local-data when it is true, so its
			// absence is what deleteFiles=false looks like on the wire.
			if v, present := reqs[0]["delete-local-data"]; present && v != false {
				t.Errorf("delete-local-data = %v: Bindery must never ask Transmission to delete the payload", v)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0].status != models.StateImported || seen[0].libraryFiles != 1 {
				t.Errorf("at the moment of removal the download was %+v, want imported with the file in the library", seen)
			}
		})
	}
}

// TestCheckTransmissionDownloads_RemoveOnImportRefusedKeepsImport has the
// daemon reject the removal (HTTP 200 with a non-success result, which is how
// Transmission says no). The import stays, the refusal is logged.
func TestCheckTransmissionDownloads_RemoveOnImportRefusedKeepsImport(t *testing.T) {
	fx := newRemoveOnImportFixture(t)
	fake := &fakeTransmission{t: t, torrents: []map[string]any{fx.transmissionTorrent()}, removeResult: "torrent not found"}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := fx.createClient(t, srv, "transmission", true)
	dl := fx.createDownload(t, client, models.StateDownloading, removeOnImportHash)
	logBuf := captureRemoveOnImportLog(t)

	fx.s.checkTransmissionDownloads(fx.ctx, client)

	got := fx.status(t, dl.GUID)
	if got.Status != models.StateImported || got.ErrorMessage != "" {
		t.Fatalf("a refused removal touched the import: status = %q, error = %q", got.Status, got.ErrorMessage)
	}
	if len(fake.removeRequests()) != 1 {
		t.Errorf("torrent-remove calls = %d, want 1", len(fake.removeRequests()))
	}
	lines := cleanupFailedLines(logBuf.String())
	if !strings.Contains(lines, "torrent not found") || !strings.Contains(lines, "transmission") {
		t.Errorf("expected a cleanup failed warning carrying the daemon's reason, got:\n%s", lines)
	}
}

// TestTryImportTransmission_RemoveOnImportRefusesLegacyNumericID covers a row
// that still stores Transmission's session-scoped numeric id. The import
// itself goes ahead, but removal by that number is refused (the number may
// belong to another torrent after a daemon restart), so the daemon must not
// see any torrent-remove at all.
func TestTryImportTransmission_RemoveOnImportRefusesLegacyNumericID(t *testing.T) {
	fx := newRemoveOnImportFixture(t)
	fake := &fakeTransmission{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := fx.createClient(t, srv, "transmission", true)
	dl := fx.createDownload(t, client, models.StateCompleted, "17")
	logBuf := captureRemoveOnImportLog(t)

	fx.s.tryImportTransmission(fx.ctx, downloader.TransmissionFor(client), client, dl, fx.downloadDir, nil)

	if got := fx.status(t, dl.GUID); got.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	if reqs := fake.removeRequests(); len(reqs) != 0 {
		t.Errorf("legacy numeric id was sent to the daemon for removal: %v", reqs)
	}
	lines := cleanupFailedLines(logBuf.String())
	if !strings.Contains(lines, "remove torrent 17") || !strings.Contains(lines, "predates info hash tracking") {
		t.Errorf("expected the refusal to be logged as a cleanup failure, got:\n%s", lines)
	}
}

// TestTryImportTransmission_RemoveOnImportSkippedWhenImportFails: the
// download directory holds no book file, so nothing is imported and nothing
// is removed.
func TestTryImportTransmission_RemoveOnImportSkippedWhenImportFails(t *testing.T) {
	fx := newRemoveOnImportFixture(t)
	if err := os.Remove(fx.sourceFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.downloadDir, "release.nfo"), []byte("no book here"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeTransmission{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := fx.createClient(t, srv, "transmission", true)
	dl := fx.createDownload(t, client, models.StateCompleted, removeOnImportHash)

	fx.s.tryImportTransmission(fx.ctx, downloader.TransmissionFor(client), client, dl, fx.downloadDir, nil)

	if got := fx.status(t, dl.GUID); got.Status != models.StateImportFailed {
		t.Fatalf("status = %q (%s), want importFailed for an import with no book file", got.Status, got.ErrorMessage)
	}
	if reqs := fake.removeRequests(); len(reqs) != 0 {
		t.Errorf("import failed but the daemon got %d torrent-remove call(s): %v", len(reqs), reqs)
	}
}
