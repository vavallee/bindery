package api

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
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
	//
	// openServable also refuses a book file that is itself a symlink or a
	// special file; see its doc for the full rule.
	sp, err := h.openServable(r.Context(), filePath)
	if writeServeError(w, r, err) {
		return
	}
	defer sp.Close()

	name := filepath.Base(filepath.Clean(filePath))
	if sp.info.IsDir() {
		streamZip(w, sp.root, sp.rel, name)
		return
	}

	f, info, err := sp.openFile()
	if writeServeError(w, r, err) {
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Disposition", contentDisposition(name))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// writeServeError answers a request whose openServable or openFile failed and
// reports whether it did. Only a path that does not exist is a 404; any other
// filesystem error (permission denied, EIO, a stale NFS handle) is a 500 so it
// is not mistaken for a file that is gone.
func writeServeError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errServeNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found on disk"})
	case errors.Is(err, errServeOutside), errors.Is(err, errServeNotRegular):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
	default:
		writeServerError(w, r, err)
	}
	return true
}

// Sentinels from openServable and servedPath.openFile. errServeOutside and
// errServeNotRegular are both a 403 to the caller; they are separate so the
// log says which rule fired. errServeNotFound is returned ONLY for
// fs.ErrNotExist: the Calibre bridge reads it as "the file is gone" and
// permanently skips the delivery, so a transient error must never map to it.
var (
	errServeOutside    = errors.New("path is outside every library root")
	errServeNotRegular = errors.New("path is a symlink or not a regular file")
	errServeNotFound   = errors.New("file not found on disk")
)

// serveStatErr maps a filesystem error to errServeNotFound when the path does
// not exist, and wraps anything else so callers answer 500.
func serveStatErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errServeNotFound
	}
	return fmt.Errorf("stat library path: %w", err)
}

// servedPath is a library path opened for serving. For a file, root is an
// os.Root on the file's parent folder and rel its name; for a folder, root is
// on the folder itself and rel is ".". Every read goes through root.
type servedPath struct {
	root *os.Root
	rel  string
	info fs.FileInfo // a regular file or a directory, never a link
}

func (s *servedPath) Close() { _ = s.root.Close() }

// openFile opens the regular file s names and returns the handle and its
// stat. The handle must be the same file openServable checked with Lstat: if
// the name was swapped for a link (or anything else) in between, it is
// refused rather than served.
func (s *servedPath) openFile() (*os.File, fs.FileInfo, error) {
	if !s.info.Mode().IsRegular() {
		return nil, nil, errServeNotRegular
	}
	f, err := s.root.Open(s.rel)
	if err != nil {
		return nil, nil, serveStatErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("stat open library file: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, s.info) {
		_ = f.Close()
		return nil, nil, errServeNotRegular
	}
	return f, info, nil
}

// openServable is the gate every route that hands library bytes to a client
// goes through (book download, the audiobook zip, OPDS, the Calibre bridge).
// The rule:
//
//   - the path as stored must be lexically under a configured root
//     (errServeOutside);
//   - a directory symlink anywhere in the path is followed. Links like
//     /books/Author -> /mnt/disk2/Author are operator configuration, imports
//     write through them, and downloads can no longer place links in the
//     library (MoveDownloadDirCtx), so a directory link there is the
//     operator's own. A linked library root works the same way;
//   - the book file itself must be a regular file by Lstat: not a symlink,
//     not a device, fifo or socket (errServeNotRegular). A file link is how a
//     download would smuggle in "cover.jpg -> /config/bindery.db";
//   - an audiobook folder is zipped with only its regular files; links inside
//     it, to files or folders, are skipped (streamZip).
//
// Only a path that does not exist is errServeNotFound; any other filesystem
// error is wrapped so the caller answers 500.
func (h *FileHandler) openServable(ctx context.Context, p string) (*servedPath, error) {
	roots, ok := h.libraryRoots(ctx)
	if !ok {
		return nil, errServeOutside
	}
	clean := filepath.Clean(p)
	if !containedUnder(clean, roots) {
		return nil, errServeOutside
	}
	li, err := os.Lstat(clean)
	if err != nil {
		return nil, serveStatErr(err)
	}
	switch {
	case li.Mode().IsRegular():
		parent, err := os.OpenRoot(filepath.Dir(clean))
		if err != nil {
			return nil, serveStatErr(err)
		}
		name := filepath.Base(clean)
		info, err := parent.Lstat(name)
		if err != nil {
			_ = parent.Close()
			return nil, serveStatErr(err)
		}
		if !info.Mode().IsRegular() {
			_ = parent.Close()
			return nil, errServeNotRegular
		}
		return &servedPath{root: parent, rel: name, info: info}, nil
	case li.IsDir(), li.Mode()&fs.ModeSymlink != 0:
		// A folder, possibly reached through an operator's folder link. A
		// link to anything but a folder is a file link and is refused.
		if li.Mode()&fs.ModeSymlink != 0 {
			st, err := os.Stat(clean)
			if err != nil {
				return nil, serveStatErr(err)
			}
			if !st.IsDir() {
				slog.Warn("file download: refusing to serve a symlinked file", "path", clean)
				return nil, errServeNotRegular
			}
		}
		dir, err := os.OpenRoot(clean)
		if err != nil {
			return nil, serveStatErr(err)
		}
		info, err := dir.Stat(".")
		if err != nil {
			_ = dir.Close()
			return nil, serveStatErr(err)
		}
		return &servedPath{root: dir, rel: ".", info: info}, nil
	default:
		slog.Warn("file download: refusing to serve a special file", "path", clean, "type", li.Mode().Type().String())
		return nil, errServeNotRegular
	}
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
//
// The legacy value is an RFC 7230 quoted-string, so a backslash is escaped
// before a quote: escaping only the quote let a name ending in a backslash, or
// holding `\"`, close the string early and append parameters of its own.
func contentDisposition(name string) string {
	ascii := quotedStringEscaper.Replace(asciiFallback(name))
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + rfc5987Escape(name)
}

// quotedStringEscaper escapes the two characters a quoted-string reserves.
// strings.Replacer works left to right over the input in one pass, so the
// backslash a quote gains is never escaped a second time.
var quotedStringEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// rfc5987Escape percent-encodes every byte of s outside RFC 5987 attr-char.
// url.PathEscape, used before, leaves characters such as "=", "(", ")", ","
// and "'" unencoded, all of which a strict parser rejects inside an
// ext-value, so a name like "Title (2020).epub" produced a header that
// mime.ParseMediaType could not read.
func rfc5987Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isRFC5987AttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// isRFC5987AttrChar: ALPHA / DIGIT / "!" / "#" / "$" / "&" / "+" / "-" / "." /
// "^" / "_" / "`" / "|" / "~".
func isRFC5987AttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
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

// libraryRoots returns the roots a download may be served from, cleaned.
// ok is false when the list could not be built, and callers deny. An empty
// list (no roots configured at all) denies too: a production install missing
// BINDERY_LIBRARY_DIR must not silently degrade to "serve any path on disk".
// Tests that need an unscoped handler must seed allowedRoots explicitly (e.g.
// t.TempDir()).
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
// FAIL CLOSED: if listing root folders errors, it is logged and the request
// is denied. A transient DB hiccup never widens the allow-list.
func (h *FileHandler) libraryRoots(ctx context.Context) ([]string, bool) {
	roots := make([]string, 0, len(h.allowedRoots)+2)
	for _, r := range h.allowedRoots {
		roots = append(roots, filepath.Clean(r))
	}
	if h.rootFolders != nil {
		folders, err := h.rootFolders.List(ctx)
		if err != nil {
			slog.Error("file download: failed to list root folders for allow-list, denying", "error", err)
			return nil, false
		}
		for _, f := range folders {
			roots = append(roots, filepath.Clean(f.Path))
		}
	}
	return roots, len(roots) > 0
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

// streamZip writes a zip archive of every regular file under dir (a path
// inside root) to the ResponseWriter, named name.zip. Headers are set before
// the first byte is written. Content-Length is unknown (streamed), so we use
// chunked transfer.
//
// The walk reads directory entries without following links, and anything
// that is not a regular file (a symlink to a file or a directory, a device, a
// fifo) is skipped rather than opened. Every open goes through root, so even a
// file swapped for a link mid-walk cannot be read from outside the library.
func streamZip(w http.ResponseWriter, root *os.Root, dir, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name+".zip"))

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	fsys := root.FS()
	base := filepath.ToSlash(dir)
	_ = fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			slog.Warn("file download: skipping a symlink or special file in a zipped folder",
				"root", root.Name(), "path", p, "type", d.Type().String())
			return nil
		}
		f, ferr := fsys.Open(p)
		if ferr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		if info, serr := f.Stat(); serr != nil || !info.Mode().IsRegular() {
			return nil
		}
		// Entry names are relative to the zipped folder, with forward slashes
		// (fs paths already are) for cross-platform extraction.
		rel := p
		if base != "." {
			rel = strings.TrimPrefix(p, base+"/")
		}
		zf, zerr := zw.Create(rel)
		if zerr != nil {
			return zerr
		}
		_, err = io.Copy(zf, f)
		return err
	})
}
