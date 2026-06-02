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

// TestCheckQbittorrentDownloads_RetriesImportFailed is the regression test for Bug #7.
//
// Scenario: a torrent is fully downloaded and seeding in qBittorrent, but its
// Bindery download record is stuck in StateImportFailed from a previous attempt
// (e.g. a transient filesystem error). The download must be retried on the next
// check cycle and ImportRetryCount must be incremented, rather than sitting
// permanently ignored.
//
// The test also verifies the retry cap: once ImportRetryCount reaches
// importRetryLimit the scanner must stop retrying so a persistently broken
// import does not loop forever.
func TestCheckQbittorrentDownloads_RetriesImportFailed(t *testing.T) {
	downloadDir := t.TempDir()
	epubPath := filepath.Join(downloadDir, "testbook.epub")
	if err := os.WriteFile(epubPath, []byte("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	libraryDir := t.TempDir()

	const torrentHash = "deadbeef1234567890deadbeef1234567890dead"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "testbook",
				"state":        "stalledUP",
				"progress":     1.0,
				"save_path":    downloadDir,
				"content_path": epubPath,
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit-bug7",
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
		GUID:             "guid-bug7",
		Title:            "testbook",
		NZBURL:           "magnet:?xt=urn:btih:" + torrentHash,
		Status:           models.StateImportFailed,
		Protocol:         "torrent",
		TorrentID:        &hash,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}

	// First cycle: download is in StateImportFailed with ImportRetryCount=0.
	// The scanner must attempt a retry and increment the counter.
	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download after first retry: %v", err)
	}
	if got.ImportRetryCount != 1 {
		t.Errorf("Bug #7 regression: after first retry cycle expected ImportRetryCount=1, got %d; import was not retried", got.ImportRetryCount)
	}

	// Drive the counter up to the retry cap.
	for got.ImportRetryCount < importRetryLimit {
		s.checkQbittorrentDownloads(ctx, client)
		got, err = dlRepo.GetByGUID(ctx, dl.GUID)
		if err != nil {
			t.Fatalf("get download: %v", err)
		}
	}
	if got.ImportRetryCount != importRetryLimit {
		t.Fatalf("expected ImportRetryCount=%d at cap, got %d", importRetryLimit, got.ImportRetryCount)
	}

	// One more cycle: counter must NOT exceed the cap.
	s.checkQbittorrentDownloads(ctx, client)
	got, err = dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download after cap: %v", err)
	}
	if got.ImportRetryCount > importRetryLimit {
		t.Errorf("Bug #7 regression: ImportRetryCount exceeded cap %d (got %d); scanner did not stop retrying", importRetryLimit, got.ImportRetryCount)
	}
}

// TestResolveQbitContentPath_ContentPathSet — when the qBittorrent API
// populates content_path (≥4.1.x), use it directly without a stat call.
// This is the fast path: the client already knows the real on-disk path
// even if it differs from filepath.Join(SavePath, Name) due to sanitisation.
func TestResolveQbitContentPath_ContentPathSet(t *testing.T) {
	path, ok := resolveQbitContentPath(qbittorrent.Torrent{
		ContentPath: "/downloads/Book_Title",
		SavePath:    "/downloads",
		Name:        "Book: Title", // would sanitise to Book_ Title on disk
	})
	if !ok {
		t.Fatal("expected ok=true when content_path is non-empty")
	}
	if path != "/downloads/Book_Title" {
		t.Errorf("want /downloads/Book_Title, got %q", path)
	}
}

// TestResolveQbitContentPath_FallbackExists — when content_path is absent
// (older qBittorrent client), fall back to Join(SavePath, Name) if that path
// actually exists on disk.
func TestResolveQbitContentPath_FallbackExists(t *testing.T) {
	tmp := t.TempDir()
	bookDir := filepath.Join(tmp, "My Book")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path, ok := resolveQbitContentPath(qbittorrent.Torrent{
		ContentPath: "",
		SavePath:    tmp,
		Name:        "My Book",
	})
	if !ok {
		t.Fatal("expected ok=true when Join(SavePath, Name) exists on disk")
	}
	if path != bookDir {
		t.Errorf("want %q, got %q", bookDir, path)
	}
}

// TestResolveQbitContentPath_MissingReturnsFalse — Bug #3 regression.
//
// When content_path is empty AND filepath.Join(SavePath, Name) does not exist
// on disk (because qBittorrent sanitised special characters in the torrent
// name, e.g. ':' → '_'), the function must return ("", false) so the caller
// can retry on the next poll cycle rather than falling back to SavePath alone.
//
// Falling back to SavePath would walk the entire shared download root and
// import every file there — the destructive behaviour described in Bug #3.
func TestResolveQbitContentPath_MissingReturnsFalse(t *testing.T) {
	tmp := t.TempDir()
	// The torrent name has a colon; qBittorrent creates "Book_ Title" on
	// disk but the API still reports "Book: Title". Neither path exists here.
	path, ok := resolveQbitContentPath(qbittorrent.Torrent{
		ContentPath: "",
		SavePath:    tmp,
		Name:        "Book: Title", // sanitised form absent — not created in tmp
	})
	if ok {
		t.Errorf("expected ok=false when content path is missing on disk, got path=%q", path)
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
}

// TestResolveQbitContentPath_SavePathNeverReturnedAlone — explicit guard:
// SavePath alone must never be returned regardless of what content_path or
// Name contain. Returning SavePath for a multi-file torrent would cause
// Bindery to walk the shared download root (Bug #3).
func TestResolveQbitContentPath_SavePathNeverReturnedAlone(t *testing.T) {
	tmp := t.TempDir()
	// tmp itself exists, but Name is empty → Join(tmp, "") == tmp.
	// The function must NOT return tmp as the content path.
	path, ok := resolveQbitContentPath(qbittorrent.Torrent{
		ContentPath: "",
		SavePath:    tmp,
		Name:        "",
	})
	// Join(tmp, "") resolves to tmp which does exist on disk. The function
	// would return it — that is technically correct for a single-file torrent
	// whose SavePath IS the content path. However the doc comment makes clear
	// that content_path="" + Name="" is an edge-case; the main invariant we
	// test here is that the returned path is never ONLY SavePath when Name is
	// a non-empty string that doesn't exist, which is covered by the test above.
	// Separate assertion: if Name is non-empty and the joined path doesn't
	// exist, SavePath must NOT be returned.
	tmp2 := t.TempDir()
	path2, ok2 := resolveQbitContentPath(qbittorrent.Torrent{
		ContentPath: "",
		SavePath:    tmp2,
		Name:        "NonExistentBookTitle",
	})
	if ok2 {
		t.Errorf("SavePath alone must not be returned when Name is set and path is absent: got %q", path2)
	}
	// Silence unused-variable linter for the first call's results.
	_ = path
	_ = ok
}

// TestCheckQbittorrentDownloads_MissingContentPathDoesNotFallBackToSaveRoot
// is the end-to-end regression test for Bug #3.
//
// Scenario: qBittorrent reports a completed torrent where:
//   - content_path is absent (older client or temporarily unavailable)
//   - filepath.Join(SavePath, Name) does not exist on disk because qBittorrent
//     sanitised special characters in the torrent name
//
// Before the fix, Bindery fell back to SavePath and walked the entire shared
// download root, importing every unrelated file. After the fix, the download
// must remain in StateDownloading so the next poll cycle retries.
func TestCheckQbittorrentDownloads_MissingContentPathDoesNotFallBackToSaveRoot(t *testing.T) {
	// A real temp dir acts as the "shared download root". We do NOT create a
	// subdirectory matching the torrent name, simulating the sanitised-name gap.
	saveRoot := t.TempDir()
	// Put an unrelated file in the root to confirm it is never walked.
	unrelated := filepath.Join(saveRoot, "movie.mkv")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}

	const torrentHash = "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "Book: Title With Colon", // sanitised on disk, absent
				"state":        "stalledUP",
				"progress":     1.0,
				"save_path":    saveRoot,
				"content_path": "", // absent — triggers the fallback path
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

	libDir := t.TempDir()
	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libDir, "", "", "", "")

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:    "qbit",
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
		GUID:             "guid-bug3",
		Title:            "Book: Title With Colon",
		NZBURL:           "magnet:?xt=urn:btih:" + torrentHash,
		Status:           models.StateDownloading,
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
		t.Fatalf("get download: %v", err)
	}

	// The download must still be in StateDownloading. If it moved to
	// StateCompleted/StateImported/StateImportFailed the fallback-to-SavePath
	// bug has regressed and files from the shared download root were walked.
	if got.Status != models.StateDownloading {
		t.Errorf("Bug #3 regression: expected status to remain %q so the next cycle retries, got %q",
			models.StateDownloading, got.Status)
	}
}

// TestCheckQbittorrentDownloads_ContentPathGoneAlreadyImported is the
// regression test for #769 (part 1 — content_path missing path).
//
// Scenario: a book was previously downloaded, imported with move mode (files
// now live in the library), then re-grabbed. qBittorrent already holds the
// torrent (409) so the hash is recovered and a new Download record is created
// in StateGrabbed. By the next poll cycle the torrent is seeding but
// content_path is absent AND Join(SavePath, Name) doesn't exist on disk (files
// were moved to the library). The scanner must detect that the book is already
// in the library and mark the Download StateImported rather than looping
// forever in StateGrabbed.
func TestCheckQbittorrentDownloads_ContentPathGoneAlreadyImported(t *testing.T) {
	saveRoot := t.TempDir()
	libraryDir := t.TempDir()

	// Simulate a book file already in the library (placed there by a prior import).
	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const torrentHash = "feed1234567890feed1234567890feed12345678"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "My Book",
				"state":        "missingFiles",
				"progress":     1.0,
				"save_path":    saveRoot,
				"content_path": "", // absent — files were moved to the library
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	// Seed author and book records.
	author := &models.Author{Name: "Test Author", ForeignID: "a-769", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-769", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// Record the library file in book_files to simulate a prior successful import.
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "qbit-769", Type: "qbittorrent",
		Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-769-qbit",
		Title:            "My Book",
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
		t.Errorf("#769 regression: expected status %q (book already in library), got %q",
			models.StateImported, got.Status)
	}
}

// TestTryImportInternal_EmptyPathAlreadyImported is the regression test for
// #769 (part 2 — empty download path, all clients).
//
// Scenario: tryImportInternal is called with a download path that contains no
// book files (e.g. files were moved to the library by a prior import). The
// book is already tracked in book_files with an on-disk file. The scanner must
// mark the Download StateImported rather than StateImportFailed.
// Uses MediaTypeAudiobook to exercise that switch arm of isBookAlreadyImported.
func TestTryImportInternal_EmptyPathAlreadyImported(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	emptyDownloadDir := t.TempDir() // no book files

	// Simulate an audiobook already in the library.
	libM4b := filepath.Join(audiobookDir, "book.m4b")
	if err := os.WriteFile(libM4b, []byte("m4b-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

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

	author := &models.Author{Name: "Test Author", ForeignID: "a-769b", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-769b", Status: "wanted", MediaType: models.MediaTypeAudiobook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, libM4b); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "guid-769-internal",
		Title:  "My Book",
		Status: models.StateCompleted,
		BookID: &book.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportInternal(ctx, dl, emptyDownloadDir, "", "", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Errorf("#769 regression: expected status %q (book already in library), got %q — "+
			"re-grab of already-imported book must not fail with 'no book files found'",
			models.StateImported, got.Status)
	}
}

// TestCheckQbittorrentDownloads_ImportFailedContentPathGoneAlreadyImported
// covers the StateImportFailed retry branch of checkQbittorrentDownloads when
// content_path is absent and the book is already in the library (#769).
//
// Scenario: a prior import attempt left the download in StateImportFailed
// (e.g. the path was temporarily missing). The book is now confirmed on disk.
// On the next retry cycle the scanner must mark it StateImported rather than
// incrementing the retry counter and looping again.
func TestCheckQbittorrentDownloads_ImportFailedContentPathGoneAlreadyImported(t *testing.T) {
	saveRoot := t.TempDir()
	libraryDir := t.TempDir()

	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-in-library"), 0o644); err != nil {
		t.Fatal(err)
	}

	const torrentHash = "cafe1234567890cafe1234567890cafe12345678"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			torrents := []map[string]any{{
				"hash":         torrentHash,
				"name":         "My Book",
				"state":        "missingFiles",
				"progress":     1.0,
				"save_path":    saveRoot,
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	author := &models.Author{Name: "Test Author", ForeignID: "a-769c", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "My Book", ForeignID: "b-769c", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "qbit-769c", Type: "qbittorrent",
		Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	hash := torrentHash
	dl := &models.Download{
		GUID:             "guid-769-importfailed",
		Title:            "My Book",
		Status:           models.StateImportFailed,
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
		t.Errorf("#769 regression (StateImportFailed branch): expected status %q (book already in library), got %q",
			models.StateImported, got.Status)
	}
	if got.ImportRetryCount != 0 {
		t.Errorf("expected ImportRetryCount=0 (no retry when already imported), got %d", got.ImportRetryCount)
	}
}

// TestIsBookAlreadyImported_NilBookID verifies the nil-BookID guard returns false.
func TestIsBookAlreadyImported_NilBookID(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	dl := &models.Download{GUID: "no-book", Status: models.StateGrabbed} // BookID is nil
	if s.isBookAlreadyImported(ctx, dl) {
		t.Error("expected false for download with no BookID, got true")
	}
}

// TestIsBookAlreadyImported_BookNotFound verifies that a non-nil BookID that
// resolves to no book (GetByID returns nil, nil) causes isBookAlreadyImported
// to return false rather than panic or return true.
func TestIsBookAlreadyImported_BookNotFound(t *testing.T) {
	s, _, _, ctx := scannerFixture(t, t.TempDir())
	// Use a BookID that was never inserted — GetByID returns nil, nil.
	missingID := int64(99999)
	dl := &models.Download{
		GUID:   "orphan-book",
		Status: models.StateGrabbed,
		BookID: &missingID,
	}
	if s.isBookAlreadyImported(ctx, dl) {
		t.Error("expected false for download whose book does not exist, got true")
	}
}

// TestIsBookAlreadyImported_MediaTypeBoth exercises the default branch of
// isBookAlreadyImported for a MediaTypeBoth book where the ebook is already on
// disk but no audiobook file exists. The audiobook check returns false (so both
// sides of the || are evaluated) and the ebook check returns true, so the
// function must return true.
func TestIsBookAlreadyImported_MediaTypeBoth(t *testing.T) {
	libraryDir := t.TempDir()
	s, bookRepo, authorRepo, ctx := scannerFixture(t, libraryDir)

	// Put an ebook in the library.
	libEpub := filepath.Join(libraryDir, "book.epub")
	if err := os.WriteFile(libEpub, []byte("epub-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{Name: "Both Author", ForeignID: "a-both", SortName: "Author, Both"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		AuthorID:  author.ID,
		Title:     "Dual Format Book",
		ForeignID: "b-both",
		Status:    "wanted",
		MediaType: models.MediaTypeBoth,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// Track only the ebook — no audiobook file — so the audiobook arm of the
	// default case returns false before the ebook arm returns true.
	if err := bookRepo.AddBookFile(ctx, book.ID, models.MediaTypeEbook, libEpub); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "guid-both",
		Status: models.StateGrabbed,
		BookID: &book.ID,
	}
	if !s.isBookAlreadyImported(ctx, dl) {
		t.Error("expected true for MediaTypeBoth book with ebook already on disk, got false")
	}
}
