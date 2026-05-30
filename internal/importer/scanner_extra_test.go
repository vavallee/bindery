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

// TestScanLibrary_ReconcilesImportedWithEmptyFilePath is the #875 repro:
// Calibre import creates books with Status=Imported, but in container
// setups where Calibre's mount differs from Bindery's view the FilePath
// stays empty. Pre-fix the scanner skipped these entirely (the candidate
// filter required Status=Wanted), so a 3700-epub library reconciled zero
// books until each author was metadata-refreshed.
func TestScanLibrary_ReconcilesImportedWithEmptyFilePath(t *testing.T) {
	libDir := t.TempDir()
	epub := filepath.Join(libDir, "Dark Matter.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "calibre:book:1", AuthorID: author.ID,
		Title: "Dark Matter", Status: models.BookStatusImported, FilePath: "",
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
		t.Errorf("expected Imported book with empty FilePath to reconcile to %q, got %q", epub, got.FilePath)
	}
}

// TestScanLibrary_ReconcilesImportedWithMissingFile covers the related
// case where the recorded FilePath points at a location the file no
// longer exists at (user moved the library, file got renamed, etc.).
// Pre-fix these orphaned rows stayed orphaned forever.
func TestScanLibrary_ReconcilesImportedWithMissingFile(t *testing.T) {
	libDir := t.TempDir()
	epub := filepath.Join(libDir, "Recursion.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL2A", Name: "B", SortName: "B"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "calibre:book:2", AuthorID: author.ID,
		Title: "Recursion", Status: models.BookStatusImported,
		FilePath: "/this/path/does/not/exist/Recursion.epub",
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
		t.Errorf("expected Imported book with stale FilePath to reconcile to %q, got %q", epub, got.FilePath)
	}
}

// TestScanLibrary_LeavesImportedWithTrackedFile verifies the candidate
// filter does not double-attach an Imported book whose file is already
// on disk and recorded in book_files. Pre-fix, an over-broad filter
// would have re-reconciled the book and the title tier would have
// rejected the duplicate at AddBookFile time (UNIQUE on path) but the
// pre-AddBookFile diagnostics would noisily log every attempted match.
//
// The book is created via AddBookFile so book_files is correctly
// populated and the scanner's trackedPaths set picks up the path. The
// scanner sees a tracked path and skips the file entirely.
func TestScanLibrary_LeavesImportedWithTrackedFile(t *testing.T) {
	libDir := t.TempDir()
	epubA := filepath.Join(libDir, "First.epub")
	if err := os.WriteFile(epubA, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OL3A", Name: "C", SortName: "C"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookA := &models.Book{
		ForeignID: "calibre:book:A", AuthorID: author.ID,
		Title: "First", Status: models.BookStatusImported,
	}
	if err := books.Create(ctx, bookA); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, bookA.ID, "ebook", epubA); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	filesA, err := books.ListFiles(ctx, bookA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(filesA) != 1 || filesA[0].Path != epubA {
		t.Errorf("bookA must keep exactly one book_files row at %q; got %d entries: %+v", epubA, len(filesA), filesA)
	}
}

// TestIsReconcileCandidate covers the candidate-filter helper in
// isolation so a future refactor that breaks the precedence of these
// cases shows up here rather than as a scanner-level surprise.
func TestIsReconcileCandidate(t *testing.T) {
	t.Run("wanted is always a candidate", func(t *testing.T) {
		if !isReconcileCandidate(&models.Book{Status: models.BookStatusWanted}) {
			t.Error("Wanted should be a candidate")
		}
		if !isReconcileCandidate(&models.Book{Status: models.BookStatusWanted, FilePath: "/already/has/file"}) {
			t.Error("Wanted should remain a candidate even with FilePath set")
		}
	})
	t.Run("imported with no recorded paths is a candidate", func(t *testing.T) {
		if !isReconcileCandidate(&models.Book{Status: models.BookStatusImported}) {
			t.Error("Imported with empty FilePath should be a candidate")
		}
	})
	t.Run("imported with nonexistent path is a candidate", func(t *testing.T) {
		b := &models.Book{Status: models.BookStatusImported, FilePath: "/no/such/file"}
		if !isReconcileCandidate(b) {
			t.Error("Imported with nonexistent FilePath should be a candidate")
		}
	})
	t.Run("imported with existing path is not a candidate", func(t *testing.T) {
		tmp := t.TempDir()
		f := filepath.Join(tmp, "exists.epub")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		b := &models.Book{Status: models.BookStatusImported, FilePath: f}
		if isReconcileCandidate(b) {
			t.Error("Imported with existing FilePath should not be a candidate")
		}
	})
	t.Run("downloading and downloaded are not candidates", func(t *testing.T) {
		for _, s := range []string{models.BookStatusDownloading, models.BookStatusDownloaded, models.BookStatusSkipped} {
			if isReconcileCandidate(&models.Book{Status: s}) {
				t.Errorf("status %q should not be a candidate", s)
			}
		}
	})
	t.Run("nil is not a candidate", func(t *testing.T) {
		if isReconcileCandidate(nil) {
			t.Error("nil book should not be a candidate")
		}
	})
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

// TestScanLibrary_AudiobookMatchesByEmbeddedASIN — a well-tagged audiobook
// with an ASIN in its ID3 tags must reconcile against the book's ASIN even
// when the filename would otherwise fail title/author matching (#303).
func TestScanLibrary_AudiobookMatchesByEmbeddedASIN(t *testing.T) {
	libDir := t.TempDir()

	// File has an uninformative name and lives in a plain folder — without
	// tag-reading, the filename-based matcher has nothing to go on.
	abDir := filepath.Join(libDir, "Audible Download")
	if err := os.MkdirAll(abDir, 0o755); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(abDir, "part1.mp3")
	if err := os.WriteFile(audioPath, buildID3v23("The Way of Kings", "Brandon Sanderson", "B003P2WO5E"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA-asin", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// The book's title deliberately does NOT share tokens with the
	// filename "part1.mp3" — the only way this reconciles is via ASIN.
	book := &models.Book{
		ForeignID: "OLB-asin", AuthorID: author.ID,
		Title: "The Way of Kings", ASIN: "B003P2WO5E",
		Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AudiobookFilePath != audioPath {
		t.Errorf("expected ASIN-matched audiobook FilePath=%q, got AudiobookFilePath=%q",
			audioPath, got.AudiobookFilePath)
	}
}

// TestScanLibrary_AudiobookMatchesByEmbeddedTitleAuthor — when no ASIN is
// present, embedded title+author tags still drive the match even if the
// filename alone wouldn't.
func TestScanLibrary_AudiobookMatchesByEmbeddedTitleAuthor(t *testing.T) {
	libDir := t.TempDir()

	audioPath := filepath.Join(libDir, "opaque.mp3")
	if err := os.WriteFile(audioPath, buildID3v23("Mistborn", "Brandon Sanderson", ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA-tt", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-tt", AuthorID: author.ID,
		Title: "Mistborn", Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AudiobookFilePath != audioPath {
		t.Errorf("expected tag-matched audiobook FilePath=%q, got AudiobookFilePath=%q",
			audioPath, got.AudiobookFilePath)
	}
}

// TestScanLibrary_AudiobookTagReadFailureFallsBack — when an audio file's
// tags cannot be read (truncated / unrecognised), the scan must fall back
// to filename parsing rather than crashing or skipping the file entirely.
func TestScanLibrary_AudiobookTagReadFailureFallsBack(t *testing.T) {
	libDir := t.TempDir()

	// Author-inferred directory layout, filename contains the title. The
	// file body is gibberish — tag.ReadFrom will error.
	bookDir := filepath.Join(libDir, "Ursula K Le Guin")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(bookDir, "A Wizard of Earthsea.mp3")
	if err := os.WriteFile(audioPath, []byte("garbage, not a real audio container"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA-fb", Name: "Ursula K Le Guin", SortName: "Le Guin, Ursula K"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-fb", AuthorID: author.ID,
		Title: "A Wizard of Earthsea", Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AudiobookFilePath != audioPath {
		t.Errorf("expected filename-fallback match to set AudiobookFilePath=%q, got %q",
			audioPath, got.AudiobookFilePath)
	}
}

// TestScanLibrary_RejectsFileOutsideLibraryRoot verifies the #343 path-constraint
// fix: a rescan must not claim a file that lives outside the candidate book's
// effective library root, even when title+author match.
func TestScanLibrary_RejectsFileOutsideLibraryRoot(t *testing.T) {
	libDir := t.TempDir()
	otherDir := t.TempDir() // separate tree — book should NOT be claimed from here

	// Put the orphan under otherDir, not under libDir.
	epub := filepath.Join(otherDir, "Dune.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also put a correctly-located file under libDir so the walk finds it.
	localEpub := filepath.Join(libDir, "Dune-local.epub")
	if err := os.WriteFile(localEpub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, libDir)
	author := &models.Author{ForeignID: "OLA-343", Name: "Frank Herbert", SortName: "Herbert, Frank"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-343", AuthorID: author.ID,
		Title: "Dune", Status: models.BookStatusWanted,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Only scan libDir — otherDir is outside. The scanner should reconcile
	// localEpub (inside libDir) and ignore epub (outside libDir).
	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	// localEpub is inside libDir so it should have been claimed.
	if got.FilePath != localEpub {
		t.Errorf("expected FilePath=%q, got %q", localEpub, got.FilePath)
	}
	// The file from outside the root must NOT be registered in book_files.
	files, _ := books.ListFiles(ctx, book.ID)
	for _, f := range files {
		if f.Path == epub {
			t.Errorf("file outside library root was incorrectly claimed: %q", epub)
		}
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
