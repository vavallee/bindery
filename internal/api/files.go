package api

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type FileHandler struct {
	books        *db.BookRepo
	allowedRoots []string
	// rootFolders, when set, is consulted at REQUEST TIME so the download
	// allow-list also covers user-configured root folders (rows in the
	// root_folders table). The importer writes book files under these roots
	// — which can live on a different mount than the static LibraryDir /
	// AudiobookDir — so a startup-captured static list would wrongly deny
	// downloads for the standard root-folder setup, and would miss folders
	// created after boot. nil disables the dynamic check (static roots only).
	rootFolders *db.RootFolderRepo
}

func NewFileHandler(books *db.BookRepo, allowedRoots ...string) *FileHandler {
	var roots []string
	for _, r := range allowedRoots {
		if r != "" {
			roots = append(roots, filepath.Clean(r))
		}
	}
	return &FileHandler{books: books, allowedRoots: roots}
}

// WithRootFolders attaches the root folder repo so the download allow-list can
// include user-configured root folder paths resolved at request time. Mirrors
// the scanner's WithRootFolders wiring (internal/importer/scanner.go) for
// consistency. Returns the handler for chaining. Safe to omit: when unset only
// the static roots (LibraryDir / AudiobookDir) are allowed.
func (h *FileHandler) WithRootFolders(rf *db.RootFolderRepo) *FileHandler {
	h.rootFolders = rf
	return h
}

// Download serves the book's content for browser download.
//   - Ebook (FilePath is a file): streams the file with its original name.
//   - Audiobook (FilePath is a directory): streams a zip of the folder so
//     multi-part m4b/mp3 + cover art come down as one bundle.
//   - ?path= serves one specific tracked file, for a book that holds several
//     of the same format.
//   - ?format=ebook|audiobook picks that format's file on dual-format books;
//     without it the legacy FilePath wins, then ebook, then audiobook.
//
// ?path= exists because the two format columns cannot express a book with more
// than one file of a format (#2408). EbookFilePath is a single column, so a
// book holding three epubs had exactly one reachable download, and the book
// page could only offer a per-format link because that was all the endpoint
// could answer. book_files has carried the full inventory since migration 028;
// this is the selector that reaches it.
//
// Same contract as DeleteFile's ?path= (see deregisterBookFile in books.go):
// the value is resolved against this book's book_files rows and refused if it
// is not one of them, so the query string selects among a known set rather
// than naming a path on disk. When both are supplied ?path= wins, being the
// more specific of the two; the UI sends one or the other, never both.
func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	book, err := h.books.GetByID(r.Context(), id)
	if err != nil || book == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}
	// Tier-1 cross-user IDOR guard (D1). Hit by OPDS readers too; respond
	// 404 (not 403) on mismatch so we do not change the response shape and
	// do not leak existence to non-owners.
	if !auth.CheckOwnership(r.Context(), book.OwnerUserID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
		return
	}

	// Optional ?path= selects one specific file the book actually holds. The
	// requested value is matched against this book's book_files rows and the
	// STORED spelling is what gets served, so a caller that cleaned the path
	// (or copied one with a trailing slash off an audiobook directory out of a
	// JSON dump) still resolves, and a caller that invented one does not.
	//
	// Refusing with 404 rather than falling back to the format chain is
	// deliberate: a silent fallback would hand back a different file than the
	// one asked for, under a filename the caller did not choose, which is a
	// worse failure than an error for anything scripted against this endpoint.
	if requested := r.URL.Query().Get("path"); requested != "" {
		resolved, err := h.trackedFilePath(r.Context(), id, requested)
		if err != nil {
			writeServerError(w, r, err)
			return
		}
		if resolved == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "path is not tracked against this book",
			})
			return
		}
		h.serveFile(w, r, resolved)
		return
	}

	// Optional ?format=ebook|audiobook scopes the download to one format on
	// dual-format books (same query param contract as DeleteFile). Without it,
	// keep the legacy FilePath-first chain so single-format books and rows
	// predating the dual-format schema (migration 026) behave as before.
	var filePath string
	switch format := r.URL.Query().Get("format"); format {
	case "":
		filePath = book.FilePath
		if filePath == "" {
			filePath = book.EbookFilePath
		}
		if filePath == "" {
			filePath = book.AudiobookFilePath
		}
	case models.MediaTypeEbook:
		filePath = book.EbookFilePath
		if filePath == "" {
			filePath = legacyPathForFormat(book.FilePath, false)
		}
	case models.MediaTypeAudiobook:
		filePath = book.AudiobookFilePath
		if filePath == "" {
			filePath = legacyPathForFormat(book.FilePath, true)
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid format"})
		return
	}
	if filePath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no file available for this book"})
		return
	}

	h.serveFile(w, r, filePath)
}

// trackedFilePath resolves requested against the book's book_files rows and
// returns the STORED spelling, or "" when the book does not track that path.
// Comparison is on filepath.Clean of both sides, matching deregisterBookFile:
// book_files stores cleaned paths, but a caller round-tripping one through JSON
// can pick up a trailing separator on an audiobook directory.
//
// This is the authorisation step for ?path=, not a convenience. It is what
// keeps a client-supplied path from naming a file on disk: the value only ever
// selects among rows already recorded against this book, which the ownership
// check above has already confirmed belongs to the caller.
func (h *FileHandler) trackedFilePath(ctx context.Context, bookID int64, requested string) (string, error) {
	files, err := h.books.ListFiles(ctx, bookID)
	if err != nil {
		return "", err
	}
	want := filepath.Clean(requested)
	for _, f := range files {
		if filepath.Clean(f.Path) == want {
			return f.Path, nil
		}
	}
	return "", nil
}

// serveFile streams one resolved path: a directory as a zip bundle, a regular
// file with its own name. Shared by the ?path= and format-chain branches so
// both go through the same allow-list check rather than one growing a bypass.
func (h *FileHandler) serveFile(w http.ResponseWriter, r *http.Request, filePath string) {
	// Defence-in-depth: refuse to serve paths that aren't under a configured
	// library root, even if a tampered DB row or importer bug set a path
	// to something outside the library (e.g. /etc/passwd, /config/*).
	//
	// This runs for a ?path= request too, even though the path came out of
	// book_files rather than off the wire. A row written by an older importer
	// bug is exactly the case the check exists for, and "it was in the
	// database" is not the same as "it is inside the library".
	if !h.isAllowedPath(r.Context(), filePath) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found on disk"})
		return
	}

	if info.IsDir() {
		streamZip(w, filePath)
		return
	}

	filename := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeFile(w, r, filePath)
}

// legacyPathForFormat returns the legacy single FilePath when its on-disk
// shape matches the requested format — a directory is an audiobook bundle, a
// regular file is an ebook — and "" otherwise. Books imported before the
// dual-format schema only populate FilePath, so a format-scoped download has
// to infer which format that path actually holds rather than serve whatever
// is there.
func legacyPathForFormat(p string, wantDir bool) string {
	if p == "" {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() != wantDir {
		return ""
	}
	return p
}

// contentDisposition builds an RFC 6266 / RFC 5987 attachment header that
// works for both ASCII-clean and Unicode filenames. The legacy filename=
// parameter carries an ASCII-only fallback for ancient clients; the
// filename*= parameter carries the original UTF-8 percent-encoded for
// anything modern (every browser since ~2010).
func contentDisposition(name string) string {
	ascii := asciiFallback(name)
	disp := `attachment; filename="` + strings.ReplaceAll(ascii, `"`, `\"`) + `"`
	disp += `; filename*=UTF-8''` + url.PathEscape(name)
	return disp
}

// asciiFallback returns name with non-ASCII bytes replaced by '_' so it
// is safe to embed in the legacy quoted filename= parameter. Quotes and
// backslashes are preserved (and escaped by the caller).
func asciiFallback(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r > 0x7e || r < 0x20 {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isAllowedPath reports whether p falls under one of the configured library
// roots. Paths are compared after filepath.Clean so trailing slashes and
// `..` traversal don't bypass the check. Fails CLOSED when no roots are
// configured — a production install missing BINDERY_LIBRARY_DIR should not
// silently degrade to "serve any path on disk". Tests that need an unscoped
// handler must seed allowedRoots explicitly (e.g. t.TempDir()).
//
// Two sources of roots are consulted:
//
//   - The static roots (LibraryDir / AudiobookDir) captured at construction.
//   - When a root folder repo is wired, the user-configured root folders
//     (rows in the root_folders table), resolved at REQUEST TIME so folders
//     added/removed after boot are honoured. The importer writes book files
//     under these roots, so a book's file_path can legitimately live under any
//     of them.
//
// FAIL CLOSED: if listing root folders errors, it is logged and treated as
// "no additional roots" — a transient DB hiccup never widens the allow-list.
func (h *FileHandler) isAllowedPath(ctx context.Context, p string) bool {
	p = filepath.Clean(p)

	// Static roots first — the common case and the only path that works
	// without the repo wired.
	if containedUnder(p, h.allowedRoots) {
		return true
	}

	// Dynamic root folders, resolved per request.
	if h.rootFolders != nil {
		folders, err := h.rootFolders.List(ctx)
		if err != nil {
			slog.Error("file download: failed to list root folders for allow-list, denying", "error", err)
			return false
		}
		for _, f := range folders {
			if pathContains(filepath.Clean(f.Path), p) {
				return true
			}
		}
	}

	return false
}

// containedUnder reports whether p (already cleaned) sits under any of roots.
func containedUnder(p string, roots []string) bool {
	for _, root := range roots {
		if pathContains(filepath.Clean(root), p) {
			return true
		}
	}
	return false
}

// pathContains reports whether p is root itself or strictly nested under it.
// root and p must already be filepath.Cleaned. The separator boundary check
// ensures /lib/books does NOT match /lib/books-secret/x.
func pathContains(root, p string) bool {
	if root == "" || root == "." {
		return false
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// streamZip writes a zip archive of every regular file under srcDir to the
// ResponseWriter. Headers are set before the first byte is written.
// Content-Length is unknown (streamed), so we use chunked transfer.
func streamZip(w http.ResponseWriter, srcDir string) {
	zipName := filepath.Base(srcDir) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+zipName+"\"")

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	_ = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(srcDir, path)
		if rerr != nil {
			return nil
		}
		// Use forward slashes in zip entries for cross-platform extraction.
		rel = strings.ReplaceAll(rel, string(filepath.Separator), "/")
		zf, zerr := zw.Create(rel)
		if zerr != nil {
			return zerr
		}
		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(zf, f)
		return err
	})
}
