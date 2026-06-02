package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestImport_SingleFileTorrentSkipsSiblingFiles is the end-to-end regression
// test for issue #903.
//
// Scenario: Transmission reports a completed torrent whose single file (an
// .m4b audiobook) sits directly inside the shared download root. The same
// shared root holds unrelated sibling files (other books seeded by the
// user). Before the fix, Bindery walked downloadDir and imported every
// recognised book file there, dragging the unrelated siblings into the
// library. With the file-list API the importer asks Transmission which
// files belong to THIS torrent and only stages those.
func TestImport_SingleFileTorrentSkipsSiblingFiles(t *testing.T) {
	// One shared download root with three book files: the torrent's own
	// file plus two unrelated siblings that must NOT be imported.
	sharedDownloadDir := t.TempDir()
	ownFile := filepath.Join(sharedDownloadDir, "the-book.m4b")
	siblingA := filepath.Join(sharedDownloadDir, "other-book-a.m4b")
	siblingB := filepath.Join(sharedDownloadDir, "unrelated-ebook.epub")
	for _, p := range []string{ownFile, siblingA, siblingB} {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()

	// Transmission stub. torrent-get for fields=[id, files] returns ONLY
	// "the-book.m4b" so the importer must not pick up the siblings.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method != "torrent-get" {
			_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
			return
		}
		fields, _ := body.Arguments["fields"].([]any)
		hasFiles := false
		for _, f := range fields {
			if f == "files" {
				hasFiles = true
				break
			}
		}
		if hasFiles {
			_, _ = w.Write([]byte(`{
				"result":"success",
				"arguments":{
					"torrents":[{
						"id":42,
						"files":[{"name":"the-book.m4b","length":4,"bytesCompleted":4}]
					}]
				}
			}`))
			return
		}
		// listing call (the one driven by GetTorrents)
		torrents := []map[string]any{{
			"id":          42,
			"hashString":  "h",
			"name":        "the-book",
			"percentDone": 1.0,
			"status":      3,
			"downloadDir": sharedDownloadDir,
		}}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    "success",
			"arguments": map[string]any{"torrents": torrents},
		})
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

	author := &models.Author{Name: "Test Author", ForeignID: "a-903", SortName: "Author, Test"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "The Book", ForeignID: "b-903", Status: "wanted", MediaType: models.MediaTypeAudiobook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name: "transmission-903", Type: "transmission",
		Host: host, Port: port, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	torrentID := "42"
	dl := &models.Download{
		GUID:             "guid-903",
		Title:            "The Book",
		Status:           models.StateDownloading,
		Protocol:         "torrent",
		TorrentID:        &torrentID,
		BookID:           &book.ID,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("issue #903: expected StateImported, got %q; the file-list path may not have been taken", got.Status)
	}

	// The book's audiobook file must point inside the library, not somewhere
	// derived from the shared download root.
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("list book files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected a book file entry after a successful import")
	}
	// Walk the audiobook output dir and assert ONLY the torrent's own file
	// landed there. The two sibling files must not appear in the library.
	var landed []string
	if err := filepath.Walk(audiobookDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		landed = append(landed, filepath.Base(path))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range landed {
		if name != "the-book.m4b" {
			t.Errorf("issue #903 regression: sibling file %q landed in the audiobook library; expected only the-book.m4b", name)
		}
	}
	if len(landed) == 0 {
		t.Error("expected the-book.m4b to land in the audiobook library; nothing did")
	}

	// The siblings must remain untouched in the shared download dir,
	// confirming the importer never reached for them.
	for _, sibling := range []string{siblingA, siblingB} {
		if _, statErr := os.Stat(sibling); statErr != nil {
			t.Errorf("issue #903 regression: sibling %q was disturbed by the import: %v", sibling, statErr)
		}
	}
}

// TestImport_FallsBackToDirWalkWhenFilesUnavailable verifies the fallback
// behaviour: when the per-client Files() RPC returns an error the importer
// must emit a WARN log and fall back to walking the download path, rather
// than failing the import. This preserves behaviour for older client
// protocol versions and transient RPC failures.
//
// The test exercises tryImportInternal directly with explicitFiles=nil
// (the way the call site behaves after a failed Files() lookup) and asserts
// that the directory walk picks up the book file. A separate test
// (TestImport_SingleFileTorrentSkipsSiblingFiles) covers the
// happy-path file-list shape end-to-end through checkTransmissionDownloads.
func TestImport_FallsBackToDirWalkWhenFilesUnavailable(t *testing.T) {
	downloadDir := t.TempDir()
	libraryDir := t.TempDir()

	bookFile := filepath.Join(downloadDir, "fallback-book.epub")
	if err := os.WriteFile(bookFile, []byte("epub"), 0o644); err != nil {
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

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	author := &models.Author{Name: "Fallback Author", ForeignID: "a-fb", SortName: "Author, Fallback"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Fallback Book", ForeignID: "b-fb", Status: "wanted", MediaType: models.MediaTypeEbook}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "guid-fallback",
		Title:  "Fallback Book",
		Status: models.StateCompleted,
		BookID: &book.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	// explicitFiles=nil: the caller couldn't get a file list (e.g. RPC
	// error), so tryImportInternal must walk downloadDir itself. With a
	// valid book file present the import should succeed.
	s.tryImportInternal(ctx, dl, downloadDir, "transmission", "42", nil, nil)

	got, err := dlRepo.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get download: %v", err)
	}
	if got.Status != models.StateImported {
		t.Fatalf("fallback path: expected StateImported, got %q", got.Status)
	}
	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected book file recorded after fallback walk; files=%v err=%v", files, err)
	}
}

// TestResolveTorrentFiles_BuildsAbsoluteBinderyPaths verifies the helper
// that maps the downloader's per-torrent file list onto absolute Bindery-
// side paths. The path-remap rule rewrites the client view to Bindery's
// mount point; IsBookFile drops non-book entries; malformed names with
// ".." or absolute paths are skipped.
func TestResolveTorrentFiles_BuildsAbsoluteBinderyPaths(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	client := &models.DownloadClient{
		Name:      "trans",
		Type:      "transmission",
		PathRemap: "/data/downloads:/local/downloads",
	}
	files := []torrentFile{
		{Name: "MyBook/disc01.m4b", Size: 1024},
		{Name: "MyBook/cover.jpg", Size: 128}, // dropped by IsBookFile
		{Name: "MyBook/disc02.m4b", Size: 2048},
	}
	got := s.resolveTorrentFiles(client, "/data/downloads", files)
	want := []string{
		"/local/downloads/MyBook/disc01.m4b",
		"/local/downloads/MyBook/disc02.m4b",
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: want %d, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestResolveTorrentFiles_RejectsMalformedNames covers the safety guard.
// A file-list response containing an absolute path or a ".." traversal
// must be silently skipped rather than producing a path that could
// resolve outside the torrent's save path.
func TestResolveTorrentFiles_RejectsMalformedNames(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	client := &models.DownloadClient{Name: "trans", Type: "transmission"}
	files := []torrentFile{
		{Name: "/etc/passwd"},            // absolute — rejected
		{Name: "../escape.epub"},         // dotdot — rejected
		{Name: "MyBook/../outside.epub"}, // hidden dotdot — rejected
		{Name: "good.epub", Size: 1},     // accepted
	}
	got := s.resolveTorrentFiles(client, "/downloads", files)
	if len(got) != 1 {
		t.Fatalf("want 1 accepted file, got %d: %v", len(got), got)
	}
	if !strings.HasSuffix(got[0], "good.epub") {
		t.Errorf("expected only good.epub to pass, got %q", got[0])
	}
}

// TestResolveAudiobookSource_SingleFileReturnsFileItself — single-file
// torrent: source is the file itself so the not-a-directory branch of the
// audiobook flow places it inside destDir. Critically the shared download
// root is NOT used.
func TestResolveAudiobookSource_SingleFileReturnsFileItself(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	got, perFile := s.resolveAudiobookSource("/data/downloads", []string{"/data/downloads/lone.m4b"})
	if perFile {
		t.Fatal("single file: expected dir-based placement (perFile=false)")
	}
	if got != "/data/downloads/lone.m4b" {
		t.Errorf("single file: expected file itself, got %q", got)
	}
}

// TestResolveAudiobookSource_CommonSubdirAcceptedAsSource — multi-file
// torrent whose files share a strict subdir of the download root: the
// helper returns that subdir so MoveDir handles the whole folder.
func TestResolveAudiobookSource_CommonSubdirAcceptedAsSource(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	got, perFile := s.resolveAudiobookSource("/data/downloads", []string{
		"/data/downloads/MyBook/01.m4b",
		"/data/downloads/MyBook/02.m4b",
	})
	if perFile {
		t.Fatal("common subdir: expected dir-based placement (perFile=false)")
	}
	if got != "/data/downloads/MyBook" {
		t.Errorf("expected common subdir, got %q", got)
	}
}

// TestResolveAudiobookSource_SharedRootFallsBackToPerFile — multi-file
// torrent whose files sit DIRECTLY at the shared download root (no
// containing subfolder). Moving downloadPath would catch unrelated
// siblings; the helper must signal per-file placement instead.
func TestResolveAudiobookSource_SharedRootFallsBackToPerFile(t *testing.T) {
	s, _, _, _ := scannerFixture(t, t.TempDir())
	_, perFile := s.resolveAudiobookSource("/data/downloads", []string{
		"/data/downloads/disc01.m4b",
		"/data/downloads/disc02.m4b",
	})
	if !perFile {
		t.Fatal("issue #903: files at the shared root must fall back to per-file placement")
	}
}
