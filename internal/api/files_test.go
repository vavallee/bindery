package api

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func fileFixture(t *testing.T) (*FileHandler, *db.BookRepo, *models.Author, context.Context, string) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// isAllowedPath fails closed when no roots are configured (security
	// hardening sweep); fixture returns its TempDir as the configured root
	// and the test stores book file paths under it.
	root := t.TempDir()
	return NewFileHandler(books, root), books, author, ctx, root
}

func TestFileDownload_BadID(t *testing.T) {
	h, _, _, _, _ := fileFixture(t)
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/abc/download", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}
}

func TestFileDownload_NotFound(t *testing.T) {
	h, _, _, _, _ := fileFixture(t)
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/999/download", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing book: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_NoFilePath(t *testing.T) {
	h, books, author, ctx, _ := fileFixture(t)
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("no file path: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_FileMissingOnDisk(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// FilePath set to a path under the allowed root but the file doesn't
	// exist — isAllowedPath passes, os.Stat fails → 404.
	if err := books.SetFilePath(ctx, book.ID, filepath.Join(tmp, "missing.epub")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("file missing on disk: expected 404, got %d", rec.Code)
	}
}

func TestFileDownload_PathOutsideAllowedRoots(t *testing.T) {
	h, books, author, ctx, _ := fileFixture(t)
	book := &models.Book{ForeignID: "OL1C", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	// Path is outside the configured allowedRoots — isAllowedPath fails
	// closed → 403, defending against tampered DB rows or importer bugs
	// that point at /etc/passwd or anywhere else off the library tree.
	if err := books.SetFilePath(ctx, book.ID, "/etc/passwd"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("out-of-root path: expected 403, got %d", rec.Code)
	}
}

func TestFileDownload_EbookFile(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	path := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(path, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte("epub bytes")) {
		t.Errorf("body mismatch: got %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Errorf("expected Content-Disposition header")
	}
}

func TestFileDownload_NonASCIIFilename(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	name := "日本語.epub"
	path := filepath.Join(tmp, name)
	if err := os.WriteFile(path, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-JP", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	// RFC 5987 filename* parameter must carry the percent-encoded UTF-8 name.
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition missing filename* param: %q", cd)
	}
	encoded := url.PathEscape(name)
	if !strings.Contains(cd, encoded) {
		t.Errorf("Content-Disposition missing percent-encoded name %q: %q", encoded, cd)
	}
	// And the legacy filename= parameter must not contain raw non-ASCII bytes.
	for _, r := range cd {
		if r > 0x7e {
			t.Errorf("Content-Disposition contains non-ASCII byte %U: %q", r, cd)
			break
		}
	}
}

func TestFileDownload_AudiobookDirStreamsZip(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	dir := filepath.Join(tmp, "Title (2020)")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "cover.jpg"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{ForeignID: "OL2B", AuthorID: author.ID, Title: "A", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, dir); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/file/1/download", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type: got %q, want application/zip", ct)
	}

	// Unzip and check entries are present with forward slashes.
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		io.Copy(io.Discard, rc)
		rc.Close()
	}
	if !names["part1.m4b"] || !names["nested/cover.jpg"] {
		t.Errorf("missing entries in zip: %v", names)
	}
}

// ── Root-folder allow-list (request-time resolution) ────────────────────────
//
// The download allow-list must include user-configured root folders, not just
// the static LibraryDir / AudiobookDir. The importer writes book files under
// root folders, which can live on a different mount than the static dirs, so a
// book whose file_path is under a root folder must still be downloadable while
// out-of-all-roots paths and traversal attempts stay denied.

// rootFolderFileFixture builds a FileHandler with static roots (libraryDir,
// audiobookDir) AND a real RootFolderRepo wired via WithRootFolders. It returns
// the raw *sql.DB so tests can seed root_folders rows and owner_user_id columns
// the same way the production code paths would at runtime.
func rootFolderFileFixture(t *testing.T, libraryDir, audiobookDir string) (*FileHandler, *db.BookRepo, *db.RootFolderRepo, *models.Author, *sql.DB) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	rootFolders := db.NewRootFolderRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewFileHandler(books, libraryDir, audiobookDir).WithRootFolders(rootFolders)
	return h, books, rootFolders, author, database
}

// seedBookFile creates a real file at path and a book row pointing at it.
func seedBookFile(t *testing.T, books *db.BookRepo, author *models.Author, foreignID, path string) *models.Book {
	t.Helper()
	if err := os.WriteFile(path, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: foreignID, AuthorID: author.ID, Title: "T"}
	if err := books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(context.Background(), book.ID, path); err != nil {
		t.Fatal(err)
	}
	return book
}

// TestFileDownload_RootFolderPathAllowed is the core regression test: a book
// whose file lives under a configured root folder (NOT under LibraryDir /
// AudiobookDir) must be downloadable. This case FAILS against the pre-fix code,
// where the allow-list was the two static dirs captured at construction.
func TestFileDownload_RootFolderPathAllowed(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	rootDir := t.TempDir() // a separate mount, not under library/audiobook

	h, books, rootFolders, author, _ := rootFolderFileFixture(t, libraryDir, audiobookDir)
	if _, err := rootFolders.Create(context.Background(), rootDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootDir, "Author", "book.epub")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	book := seedBookFile(t, books, author, "OL-RF", path)

	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("root-folder book: expected 200, got %d (body %s)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), []byte("epub bytes")) {
		t.Errorf("body mismatch: got %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "book.epub") {
		t.Errorf("Content-Disposition missing filename: %q", cd)
	}
}

// TestFileDownload_StaticRootsStillWork guards against regression: with a root
// folder repo wired, books under the static LibraryDir / AudiobookDir must
// still download without consulting the dynamic list.
func TestFileDownload_StaticRootsStillWork(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()

	h, books, _, author, _ := rootFolderFileFixture(t, libraryDir, audiobookDir)

	libPath := filepath.Join(libraryDir, "lib.epub")
	libBook := seedBookFile(t, books, author, "OL-LIB", libPath)
	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(libBook.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("library-dir book: expected 200, got %d", rec.Code)
	}

	abPath := filepath.Join(audiobookDir, "ab.epub")
	abBook := seedBookFile(t, books, author, "OL-AB", abPath)
	rec = httptest.NewRecorder()
	h.Download(rec, downloadReq(abBook.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("audiobook-dir book: expected 200, got %d", rec.Code)
	}
}

// TestFileDownload_OutsideAllRootsDeniedWithRepo verifies that even with a root
// folder repo wired, a path under none of (library, audiobook, any root folder)
// is still denied with 403.
func TestFileDownload_OutsideAllRootsDeniedWithRepo(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	rootDir := t.TempDir()

	h, books, rootFolders, author, _ := rootFolderFileFixture(t, libraryDir, audiobookDir)
	if _, err := rootFolders.Create(context.Background(), rootDir); err != nil {
		t.Fatal(err)
	}
	// File exists but lives entirely outside every configured root.
	outside := filepath.Join(t.TempDir(), "evil.epub")
	book := seedBookFile(t, books, author, "OL-OUT", outside)

	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-all-roots: expected 403, got %d", rec.Code)
	}
}

// TestFileDownload_RootFolderSeparatorBoundary verifies the separator-boundary
// check holds for root folder paths: a root of /lib/books must NOT allow a
// sibling /lib/books-secret/x that merely shares a string prefix.
func TestFileDownload_RootFolderSeparatorBoundary(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "books")
	siblingDir := filepath.Join(base, "books-secret")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	h, books, rootFolders, author, _ := rootFolderFileFixture(t, t.TempDir(), t.TempDir())
	if _, err := rootFolders.Create(context.Background(), rootDir); err != nil {
		t.Fatal(err)
	}
	// File sits under the sibling dir that shares the "books" prefix but is
	// NOT under the root folder. Must be denied.
	path := filepath.Join(siblingDir, "leak.epub")
	book := seedBookFile(t, books, author, "OL-SIB", path)

	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sibling-prefix path: expected 403, got %d", rec.Code)
	}
}

// TestFileDownload_RootFolderTraversalDenied verifies a path that escapes the
// root folder via `..` is denied after filepath.Clean (the cleaned path is
// outside the root, so no allow-list entry matches).
func TestFileDownload_RootFolderTraversalDenied(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "books")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real secret file outside the root that the traversal aims at.
	secret := filepath.Join(base, "secret.epub")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	h, books, rootFolders, author, database := rootFolderFileFixture(t, t.TempDir(), t.TempDir())
	if _, err := rootFolders.Create(context.Background(), rootDir); err != nil {
		t.Fatal(err)
	}
	// Store a traversal path: <root>/../secret.epub. filepath.Clean resolves
	// this to base/secret.epub which is outside the root folder.
	traversal := filepath.Join(rootDir, "..", "secret.epub")
	book := &models.Book{ForeignID: "OL-TRAV", AuthorID: author.ID, Title: "T"}
	if err := books.Create(context.Background(), book); err != nil {
		t.Fatal(err)
	}
	// SetFilePath would normalise; write the raw traversal directly to be sure.
	if _, err := database.Exec("UPDATE books SET file_path=? WHERE id=?", traversal, book.ID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("traversal path: expected 403, got %d", rec.Code)
	}
}

// TestFileDownload_OwnershipEnforcedUnderRootFolder verifies the per-book
// ownership guard is untouched: a non-owner gets 404 even when the file lives
// under a valid root folder (path allow-list widened, IDOR guard unchanged).
func TestFileDownload_OwnershipEnforcedUnderRootFolder(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)

	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	rootDir := t.TempDir()

	h, books, rootFolders, author, database := rootFolderFileFixture(t, libraryDir, audiobookDir)
	if _, err := rootFolders.Create(context.Background(), rootDir); err != nil {
		t.Fatal(err)
	}
	users := db.NewUserRepo(database)
	owner, err := users.Create(context.Background(), "owner", "h1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(context.Background(), "other", "h2")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootDir, "book.epub")
	book := seedBookFile(t, books, author, "OL-OWN", path)
	if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", owner.ID, book.ID); err != nil {
		t.Fatal(err)
	}

	// Non-owner: path is under a valid root folder, but the IDOR guard must
	// still 404 (not leak the file).
	ctx := withAuthCtx(context.Background(), other.ID, "user")
	rec := httptest.NewRecorder()
	h.Download(rec, downloadReqCtx(ctx, book.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-owner under root folder: expected 404, got %d", rec.Code)
	}

	// Sanity: the owner can still download.
	ownerCtx := withAuthCtx(context.Background(), owner.ID, "user")
	rec = httptest.NewRecorder()
	h.Download(rec, downloadReqCtx(ownerCtx, book.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner under root folder: expected 200, got %d", rec.Code)
	}
}

// downloadReq builds a chi-aware GET request for the download handler with the
// {id} URL param populated.
func downloadReq(id int64) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/file/download", nil)
	return withURLParam(req, "id", strconv.FormatInt(id, 10))
}

// downloadReqCtx is downloadReq with a caller-supplied base context (e.g.
// carrying auth identity) layered under the chi route param.
func downloadReqCtx(ctx context.Context, id int64) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/file/download", nil).WithContext(ctx)
	return withURLParam(req, "id", strconv.FormatInt(id, 10))
}
