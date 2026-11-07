package api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// zipOtherBookFixture lays out alice's audiobook folder with the default
// layout's neighbours in it: her own audio and cover art (untracked), bob's
// ebook of the same title imported inside the folder, and a subfolder bob's
// book tracks as a whole.
type zipOtherBookFixture struct {
	env       *covAuEnv
	root      string
	folder    string
	aliceBook *models.Book
	bobBook   *models.Book
}

func newZipOtherBookFixture(t *testing.T) *zipOtherBookFixture {
	t.Helper()
	env := covAuSetup(t)
	root := t.TempDir()
	folder := filepath.Join(root, "Jane Doe", "Shared Title (2020)")
	mustWrite(t, filepath.Join(folder, "part1.mp3"))
	mustWrite(t, filepath.Join(folder, "cover.jpg"))
	mustWrite(t, filepath.Join(folder, "Disc 2", "part2.mp3"))
	bobEpub := mustWrite(t, filepath.Join(folder, "Shared Title - Jane Doe.epub"))
	bobDir := filepath.Join(folder, "Bob Extras")
	mustWrite(t, filepath.Join(bobDir, "bonus.mp3"))

	aliceBook := &models.Book{ForeignID: "OL-ALICE-Z", AuthorID: env.aliceA.ID, Title: "Shared Title", SortTitle: "shared", Status: models.BookStatusImported, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: env.alice}
	bobBook := &models.Book{ForeignID: "OL-BOB-Z", AuthorID: env.bobA.ID, Title: "Shared Title", SortTitle: "shared", Status: models.BookStatusImported, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: env.bob}
	for _, b := range []*models.Book{aliceBook, bobBook} {
		if err := env.books.Create(env.ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.books.AddBookFile(env.ctx, aliceBook.ID, models.MediaTypeAudiobook, folder); err != nil {
		t.Fatal(err)
	}
	if err := env.books.AddBookFile(env.ctx, bobBook.ID, models.MediaTypeEbook, bobEpub); err != nil {
		t.Fatal(err)
	}
	// Stored with a trailing separator, as Track keeps a path as given.
	if err := env.books.AddBookFile(env.ctx, bobBook.ID, models.MediaTypeAudiobook, bobDir+string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
	return &zipOtherBookFixture{env: env, root: root, folder: folder, aliceBook: aliceBook, bobBook: bobBook}
}

func zipEntryNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

// wantAliceOnly is alice's audiobook folder without what bob's book tracks.
var wantAliceOnly = []string{"Disc 2/part2.mp3", "cover.jpg", "part1.mp3"}

// TestDownloadAudiobookZipLeavesOutOtherBooksFiles: the folder zip carries
// the book's own audio and the untracked files beside it, and leaves out a
// file and a folder another book tracks. That holds with tenancy on and off,
// since the zip is this book's audiobook and nothing else.
func TestDownloadAudiobookZipLeavesOutOtherBooksFiles(t *testing.T) {
	for _, tenancy := range []bool{true, false} {
		t.Run("tenancy="+strconv.FormatBool(tenancy), func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, tenancy)
			fx := newZipOtherBookFixture(t)
			h := NewFileHandler(fx.env.books, fx.root)

			for _, role := range []string{"user", "admin"} {
				rec := httptest.NewRecorder()
				h.Download(rec, covAuRequest(http.MethodGet, "/?format=audiobook", "", map[string]string{"id": covAuID(fx.aliceBook.ID)}, fx.env.alice, role))
				if got := zipEntryNames(t, rec); !slices.Equal(got, wantAliceOnly) {
					t.Errorf("%s: zip entries = %v, want %v", role, got, wantAliceOnly)
				}
			}
		})
	}
}

// TestOPDSAudiobookZipLeavesOutOtherBooksFiles: the OPDS file route hands off
// to the same handler, so an OPDS reader gets the same filtered zip.
func TestOPDSAudiobookZipLeavesOutOtherBooksFiles(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	fx := newZipOtherBookFixture(t)
	oh := NewOPDSHandler(nil, fx.env.books, NewFileHandler(fx.env.books, fx.root))

	rec := httptest.NewRecorder()
	oh.DownloadFile(rec, covAuRequest(http.MethodGet, "/opds/book/"+covAuID(fx.aliceBook.ID)+"/file", "", map[string]string{"id": covAuID(fx.aliceBook.ID)}, fx.env.alice, "user"))
	if got := zipEntryNames(t, rec); !slices.Equal(got, wantAliceOnly) {
		t.Errorf("OPDS zip entries = %v, want %v", got, wantAliceOnly)
	}

	// Bob's book itself stays out of alice's reach on this route.
	rec = httptest.NewRecorder()
	oh.DownloadFile(rec, covAuRequest(http.MethodGet, "/", "", map[string]string{"id": covAuID(fx.bobBook.ID)}, fx.env.alice, "user"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("alice OPDS download of bob's book = %d, want 404", rec.Code)
	}
}

// TestDownloadAudiobookZipKeepsFilesUnderAnotherBooksAncestor: a book that
// tracks a folder ABOVE this book's folder (a box set holding the volume's
// folder) does not own what the zipped folder holds. Only rows inside the
// folder are left out, so the zip is not emptied.
func TestDownloadAudiobookZipKeepsFilesUnderAnotherBooksAncestor(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	boxSet := &models.Book{ForeignID: "OL-BOX", AuthorID: fx.env.aliceA.ID, Title: "Box Set", SortTitle: "box", Status: models.BookStatusImported, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: fx.env.alice}
	if err := fx.env.books.Create(fx.env.ctx, boxSet); err != nil {
		t.Fatal(err)
	}
	if err := fx.env.books.AddBookFile(fx.env.ctx, boxSet.ID, models.MediaTypeAudiobook, filepath.Dir(fx.folder)); err != nil {
		t.Fatal(err)
	}
	h := NewFileHandler(fx.env.books, fx.root)
	rec := httptest.NewRecorder()
	h.Download(rec, covAuRequest(http.MethodGet, "/?format=audiobook", "", map[string]string{"id": covAuID(fx.aliceBook.ID)}, fx.env.alice, "user"))
	if got := zipEntryNames(t, rec); !slices.Equal(got, wantAliceOnly) {
		t.Errorf("zip entries = %v, want %v", got, wantAliceOnly)
	}
}

// TestDownloadAudiobookZipMatchesOtherBooksRowsAcrossCase: on a case
// insensitive library (SMB, macOS, Windows) another book's row can spell a
// file in the folder, or the folder itself, in different case from the walk.
// Such a row still keeps the file out of the zip.
func TestDownloadAudiobookZipMatchesOtherBooksRowsAcrossCase(t *testing.T) {
	fx := newZipOtherBookFixture(t)
	mustWrite(t, filepath.Join(fx.folder, "notes.txt"))
	upperFolder := filepath.Join(filepath.Dir(fx.folder), strings.ToUpper(filepath.Base(fx.folder)))
	if err := fx.env.books.AddBookFile(fx.env.ctx, fx.bobBook.ID, models.MediaTypeEbook, filepath.Join(upperFolder, "NOTES.TXT")); err != nil {
		t.Fatal(err)
	}
	h := NewFileHandler(fx.env.books, fx.root)
	rec := httptest.NewRecorder()
	h.Download(rec, covAuRequest(http.MethodGet, "/?format=audiobook", "", map[string]string{"id": covAuID(fx.aliceBook.ID)}, fx.env.alice, "user"))
	if got := zipEntryNames(t, rec); !slices.Equal(got, wantAliceOnly) {
		t.Errorf("zip entries = %v, want %v", got, wantAliceOnly)
	}
}
