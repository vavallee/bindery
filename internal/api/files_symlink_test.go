package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A download can carry symlinks, and a library folder can end up holding one.
// The download routes serve regular files only: a link is never followed to
// its target, whether it is the tracked path itself or an entry inside an
// audiobook folder that gets zipped. A library ROOT reached through a link (a
// common Docker layout) is resolved on both sides and keeps working.

// symlinkOrSkipAPI creates a symlink, skipping where the platform refuses.
func symlinkOrSkipAPI(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

const symlinkSecret = "SECRET DATABASE BYTES"

// outsideSecret writes a file outside every library root and returns it.
func outsideSecret(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bindery.db")
	if err := os.WriteFile(p, []byte(symlinkSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileDownload_ZipSkipsSymlinks(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	secret := outsideSecret(t)
	dir := filepath.Join(tmp, "Author", "Title (2020)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkipAPI(t, secret, filepath.Join(dir, "cover.jpg"))
	symlinkOrSkipAPI(t, filepath.Dir(secret), filepath.Join(dir, "extras"))

	book := &models.Book{ForeignID: "OL-ZIPLINK", AuthorID: author.ID, Title: "A", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, dir); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(body, []byte(symlinkSecret)) {
			t.Fatalf("zip entry %s holds the bytes of a symlink target outside the library", f.Name)
		}
	}
	if len(names) != 1 || names[0] != "part1.m4b" {
		t.Fatalf("zip entries = %v, want only part1.m4b", names)
	}
}

func TestFileDownload_TrackedSymlinkRefused(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	secret := outsideSecret(t)
	inRoot := filepath.Join(tmp, "real.epub")
	if err := os.WriteFile(inRoot, []byte("real epub"), 0o644); err != nil {
		t.Fatal(err)
	}

	for i, tc := range []struct{ name, target string }{
		{"target outside the library", secret},
		{"target inside the library", inRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(tmp, "Author", "link"+strconv.Itoa(i)+".epub")
			symlinkOrSkipAPI(t, tc.target, link)
			book := &models.Book{ForeignID: "OL-LINK" + strconv.Itoa(i), AuthorID: author.ID, Title: "T"}
			if err := books.Create(ctx, book); err != nil {
				t.Fatal(err)
			}
			if err := books.SetFilePath(ctx, book.ID, link); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.Download(rec, downloadReq(book.ID))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("tracked symlink: expected 403, got %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), symlinkSecret) || strings.Contains(rec.Body.String(), "real epub") {
				t.Fatalf("tracked symlink served its target: %q", rec.Body.String())
			}
		})
	}
}

// TestFileDownload_SymlinkedLibraryRootStillServes: the library root itself is
// a link (Docker bind mounts and NAS shares often are). Book files under it
// are ordinary files and must keep downloading, single and zipped.
func TestFileDownload_SymlinkedLibraryRootStillServes(t *testing.T) {
	realLib := t.TempDir()
	linkedLib := filepath.Join(t.TempDir(), "books")
	symlinkOrSkipAPI(t, realLib, linkedLib)

	h, books, _, author, _ := rootFolderFileFixture(t, linkedLib, "")
	ctx := context.Background()

	epub := filepath.Join(linkedLib, "Author", "book.epub")
	if err := os.MkdirAll(filepath.Dir(epub), 0o755); err != nil {
		t.Fatal(err)
	}
	ebook := seedBookFile(t, books, author, "OL-LINKROOT", epub)
	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(ebook.ID))
	if rec.Code != http.StatusOK || rec.Body.String() != "epub bytes" {
		t.Fatalf("ebook under linked root: %d %q", rec.Code, rec.Body.String())
	}

	dir := filepath.Join(linkedLib, "Author", "Audio (2020)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ab := &models.Book{ForeignID: "OL-LINKROOT-AB", AuthorID: author.ID, Title: "Audio", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, ab); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, ab.ID, dir); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Download(rec, downloadReq(ab.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("audiobook under linked root: %d %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "part1.m4b" {
		t.Fatalf("zip entries under linked root: %d", len(zr.File))
	}
}

// TestContentDisposition_BackslashQuoteRoundTrips: a backslash before a quote
// must not let the name close the quoted filename= string early.
func TestContentDisposition_BackslashQuoteRoundTrips(t *testing.T) {
	for _, name := range []string{`a\"b.epub`, `trailing\`, `x"; filename="evil.html`, `plain.epub`} {
		cd := contentDisposition(name)
		disp, params, err := mime.ParseMediaType(cd)
		if err != nil {
			t.Errorf("%q: header %q does not parse: %v", name, cd, err)
			continue
		}
		if disp != "attachment" || params["filename"] != name {
			t.Errorf("%q: parsed %q %q from %q", name, disp, params["filename"], cd)
		}
		// The legacy quoted form on its own, for clients that ignore filename*.
		legacy, _, _ := strings.Cut(cd, "; filename*=")
		_, lp, err := mime.ParseMediaType(legacy)
		if err != nil {
			t.Errorf("%q: legacy part %q does not parse: %v", name, legacy, err)
			continue
		}
		if lp["filename"] != name {
			t.Errorf("%q: legacy filename = %q from %q", name, lp["filename"], legacy)
		}
	}
}

// TestFileDownload_ZipContentDispositionEscaped: the zip's name comes from the
// folder name, so it goes through the same escaping as single files.
func TestFileDownload_ZipContentDispositionEscaped(t *testing.T) {
	h, books, author, ctx, tmp := fileFixture(t)
	dir := filepath.Join(tmp, `Title "Quoted" \ (2020)`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-ZIPCD", AuthorID: author.ID, Title: "A", MediaType: models.MediaTypeAudiobook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, dir); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Download(rec, downloadReq(book.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("zip Content-Disposition %q does not parse: %v", cd, err)
	}
	if want := filepath.Base(dir) + ".zip"; params["filename"] != want {
		t.Fatalf("zip filename = %q, want %q", params["filename"], want)
	}
}
