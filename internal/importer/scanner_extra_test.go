package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// scannerFixture spins up an in-memory DB and wires a Scanner against it.
// All external clients (SABnzbd) stay nil — tests here exercise only the
// Scanner paths that don't hit the download client.
func scannerFixture(t *testing.T, libraryDir string) (*Scanner, *db.BookRepo, *db.AuthorRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)

	s := NewScanner(downloads, clients, books, authors, history, libraryDir, "", "", "", "")
	return s, books, authors, context.Background()
}

func TestNewScanner_AudiobookDirFallback(t *testing.T) {
	// When audiobookDir is empty the scanner falls back to libraryDir so
	// audiobook imports have a destination without extra configuration.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewHistoryRepo(database),
		"/lib", "", "", "", "",
	)
	if s.audiobookDir != "/lib" {
		t.Errorf("expected audiobookDir to default to libraryDir, got %q", s.audiobookDir)
	}
	if s.renamer == nil || s.remapper == nil {
		t.Error("expected renamer and remapper to be wired")
	}
}

// TestScanLibrary_EmptyLibraryDir short-circuits before touching the DB.
func TestScanLibrary_EmptyLibraryDir(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, "")
	s.ScanLibrary(ctx) // must not panic, must not read the DB
}

// TestScanLibrary_NoFiles walks an empty library dir and returns early.
func TestScanLibrary_NoFiles(t *testing.T) {
	libDir := t.TempDir()
	s, _, _, ctx := scannerFixture(t, libDir)
	s.ScanLibrary(ctx) // no panic, no error
}

// TestScanLibrary_ReconcilesMatchingBook puts an orphan .epub into the
// library that matches a "wanted" book by title; the scan should attach
// the file path to the book record.
func TestScanLibrary_ReconcilesMatchingBook(t *testing.T) {
	libDir := t.TempDir()
	// "Author Name - Dark Matter.epub" → parsed title "Author Name",
	// but the titleMatch heuristic takes significant words from both
	// sides, so we name the file just "Dark Matter.epub" to keep the
	// match unambiguous.
	epub := filepath.Join(libDir, "Dark Matter.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// "Dark Matter" vs parsed "Dark Matter" → overlap=2, minOverlap=2 → match.
	book := &models.Book{
		ForeignID: "OL1B", AuthorID: author.ID,
		Title: "Dark Matter", Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != epub {
		t.Errorf("expected book.FilePath to be set to %q, got %q", epub, got.FilePath)
	}
}

// TestScanLibrary_NonBookFilesIgnored — extensions not in bookExtensions
// (jpg, nfo, etc.) should not appear in the walked list.
func TestScanLibrary_NonBookFilesIgnored(t *testing.T) {
	libDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(libDir, "cover.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "info.nfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, _, ctx := scannerFixture(t, libDir)
	s.ScanLibrary(ctx) // no panic, nothing to reconcile
}

// TestCheckDownloads_NoEnabledClient returns silently when there's no
// download client to poll. Before v0.7 this hit a nil deref on the client.
func TestCheckDownloads_NoEnabledClient(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	s.CheckDownloads(ctx) // no panic, no DB writes
}

func TestCheckTransmissionDownloads_StoppedWithoutErrorDoesNotFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrents": []map[string]any{{
					"id":          7,
					"status":      0,
					"percentDone": 0.4,
					"downloadDir": "/downloads",
					"errorString": "",
				}},
			},
		})
	}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "transmission",
		Type:    "transmission",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	torrentID := "7"
	dl := &models.Download{
		GUID:             "guid-paused",
		DownloadClientID: &client.ID,
		Title:            "Paused Torrent",
		NZBURL:           "magnet:?xt=urn:btih:abc",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &torrentID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Fatalf("expected status to remain downloading, got %q", got.Status)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("expected no error message, got %q", got.ErrorMessage)
	}
}

func TestCheckTransmissionDownloads_StoppedWithErrorMarksFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"arguments": map[string]any{
				"torrents": []map[string]any{{
					"id":          9,
					"status":      6,
					"percentDone": 0.2,
					"downloadDir": "/downloads",
					"errorString": "tracker connection failed",
				}},
			},
		})
	}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "transmission",
		Type:    "transmission",
		Host:    host,
		Port:    port,
		Enabled: true,
	}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	torrentID := "9"
	dl := &models.Download{
		GUID:             "guid-errored",
		DownloadClientID: &client.ID,
		Title:            "Errored Torrent",
		NZBURL:           "magnet:?xt=urn:btih:def",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &torrentID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.Status != models.DownloadStatusFailed {
		t.Fatalf("expected status failed, got %q", got.Status)
	}
	if got.ErrorMessage != "tracker connection failed" {
		t.Fatalf("expected error message from Transmission, got %q", got.ErrorMessage)
	}
}

func scannerTestHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return u.Hostname(), port
}

// TestScanLibrary_NoDuplicateBookAssignment — regression test for issue #81.
// When two files in the library loosely match the same book's title, the book
// must only be reconciled to the FIRST file encountered; the second file must
// be left as "unmatched" rather than overwriting the first assignment.
// Without the reconciledBooks guard, allBooks is never mutated in memory so
// the book's in-memory status stays "wanted" and both files claim it — with
// the last write winning and destroying the correct earlier match.
func TestScanLibrary_NoDuplicateBookAssignment(t *testing.T) {
	libDir := t.TempDir()

	// Two files whose titles overlap with the same book title.
	file1 := filepath.Join(libDir, "Dune.epub")
	file2 := filepath.Join(libDir, "Dune (Alt Edition).epub")
	for _, f := range []string{file1, file2} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA", Name: "Frank Herbert", SortName: "Herbert, Frank"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB", AuthorID: author.ID,
		Title: "Dune", Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The book must have been reconciled to exactly one file.
	if got.FilePath == "" {
		t.Fatal("expected book to be reconciled to a file, got empty FilePath")
	}
	// The path that was set must be one of the two candidates — not
	// some other file — and the book must not have been overwritten with
	// a different file in a second pass.
	if got.FilePath != file1 && got.FilePath != file2 {
		t.Errorf("FilePath %q is not one of the expected candidates", got.FilePath)
	}
}

// TestScanLibrary_AudiobookDirectorySkipped — files inside an already-tracked
// audiobook directory must not trigger re-reconciliation. Before this fix the
// trackedPaths set only contained file paths; individual audio tracks inside
// a directory-level file_path were never found in the set and each track
// looked untracked, causing the scanner to try (and fail) to re-assign them
// to "wanted" books.
func TestScanLibrary_AudiobookDirectorySkipped(t *testing.T) {
	libDir := t.TempDir()

	// Simulate an audiobook folder: /libDir/Author/Title/01.mp3
	abDir := filepath.Join(libDir, "Author", "Title")
	if err := os.MkdirAll(abDir, 0o755); err != nil {
		t.Fatal(err)
	}
	track := filepath.Join(abDir, "01 - Chapter One.mp3")
	if err := os.WriteFile(track, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA2", Name: "Author", SortName: "Author"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// The audiobook book already has its file_path set to the directory
	// (matching how tryImport records audiobook imports).
	book := &models.Book{
		ForeignID: "OLB2", AuthorID: author.ID, Title: "Title",
		Status: models.BookStatusImported, FilePath: abDir,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// A second "wanted" book whose title would match the track file if the
	// directory guard weren't in place.
	wanted := &models.Book{
		ForeignID: "OLB3", AuthorID: author.ID, Title: "Chapter One",
		Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, wanted); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	// The track inside the already-tracked directory must not have been
	// assigned to the wanted book.
	got, err := books.GetByID(ctx, wanted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != "" {
		t.Errorf("wanted book must not have been reconciled to a track inside a tracked dir, got %q", got.FilePath)
	}
}

// TestScanLibrary_DirectoryAuthorInference — when a file's name provides no
// author hint, the scanner should infer the author from the first directory
// component relative to libraryDir (the standard Author/Title/file.ext layout).
// This prevents uninformative filenames like "book.epub" from matching books
// by wrong authors simply because parsedAuthor is empty and authorMatch
// returns true for everything.
func TestScanLibrary_DirectoryAuthorInference(t *testing.T) {
	libDir := t.TempDir()

	// File nested two levels deep: /libDir/Isaac Asimov/Foundation/foundation.epub
	// The filename alone gives no author clue; the directory does.
	bookDir := filepath.Join(libDir, "Isaac Asimov", "Foundation")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	epub := filepath.Join(bookDir, "foundation.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)

	asimov := &models.Author{ForeignID: "OLA3", Name: "Isaac Asimov", SortName: "Asimov, Isaac"}
	if err := authors.Create(ctx, asimov); err != nil {
		t.Fatal(err)
	}
	// A different author with a book also titled "Foundation" — should NOT match.
	other := &models.Author{ForeignID: "OLA4", Name: "Someone Else", SortName: "Else, Someone"}
	if err := authors.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	bookAsimov := &models.Book{
		ForeignID: "OLB4", AuthorID: asimov.ID, Title: "Foundation",
		Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, bookAsimov); err != nil {
		t.Fatal(err)
	}
	bookOther := &models.Book{
		ForeignID: "OLB5", AuthorID: other.ID, Title: "Foundation",
		Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, bookOther); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	// Asimov's book should be reconciled (directory says "Isaac Asimov").
	gotAsimov, _ := books.GetByID(ctx, bookAsimov.ID)
	if gotAsimov.FilePath != epub {
		t.Errorf("Asimov Foundation: want FilePath=%q, got %q", epub, gotAsimov.FilePath)
	}
	// The other author's book must not have been touched.
	gotOther, _ := books.GetByID(ctx, bookOther.ID)
	if gotOther.FilePath != "" {
		t.Errorf("Other Foundation must stay unreconciled, got FilePath=%q", gotOther.FilePath)
	}
}
