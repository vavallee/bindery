package importer

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// writeEpubAt writes a minimal EPUB (zip + OPF) with the given embedded
// metadata to path.
func writeEpubAt(t *testing.T, path, title, author, isbn string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	add("content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>`+title+`</dc:title>
    <dc:creator opf:role="aut">`+author+`</dc:creator>
    <dc:identifier>urn:isbn:`+isbn+`</dc:identifier>
  </metadata>
</package>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func unmatchedFixture(t *testing.T) (s *Scanner, downloads *db.DownloadRepo, books *db.BookRepo, authors *db.AuthorRepo, settings *db.SettingsRepo, libraryDir string, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx = context.Background()
	settings = db.NewSettingsRepo(database)
	downloads = db.NewDownloadRepo(database)
	books = db.NewBookRepo(database)
	authors = db.NewAuthorRepo(database)
	libraryDir = t.TempDir()
	s = NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors, db.NewHistoryRepo(database), libraryDir, "", "", "", "")
	s.WithSettings(settings)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	return s, downloads, books, authors, settings, libraryDir, ctx
}

// TestImport_RecoversUnmatchedViaEpubMetadata is the regression test for issue
// #1014: a download grabbed WITHOUT a book association (free-text Search grab)
// whose release filename mis-parses must still import by reading the EPUB's
// embedded metadata and matching the catalogue book.
func TestImport_RecoversUnmatchedViaEpubMetadata(t *testing.T) {
	s, downloads, books, authors, _, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "OLH", Name: "Peter F. Hamilton", SortName: "Hamilton, Peter F.", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLPS", AuthorID: author.ID, Title: "Pandora's Star", SortTitle: "pandora's star",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	downloadDir := t.TempDir()
	// Mis-namable release: the filename parser alone would split this wrong; the
	// embedded EPUB metadata must drive the match.
	releaseName := "Peter F Hamilton - [Commonwealth Saga 01] - Pandora's Star (US) (retail) (epub)"
	writeEpubAt(t, filepath.Join(downloadDir, releaseName+".epub"), "Pandora's Star", "Peter F. Hamilton", "9780345472199")

	dl := &models.Download{GUID: "guid-unmatched", Title: releaseName, BookID: nil, Status: models.StateCompleted, NZBURL: "fake://url"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	reloaded, err := downloads.GetByGUID(ctx, "guid-unmatched")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.BookID == nil || *reloaded.BookID != book.ID {
		t.Fatalf("recovered BookID = %v, want %d", reloaded.BookID, book.ID)
	}
	if reloaded.Status != models.StateImported {
		t.Errorf("status = %q, want %q", reloaded.Status, models.StateImported)
	}
	// The file must have landed somewhere under the library dir.
	var imported bool
	_ = filepath.Walk(libraryDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(p) == ".epub" {
			imported = true
		}
		return nil
	})
	if !imported {
		t.Errorf("no .epub landed under the library dir %s", libraryDir)
	}
}

// TestImport_UnmatchedWithNoCatalogueMatchFails confirms we do NOT guess: with
// no matching catalogue book, the import fails (for manual intervention) rather
// than associating against the wrong book.
func TestImport_UnmatchedWithNoCatalogueMatchFails(t *testing.T) {
	s, downloads, _, _, _, _, ctx := unmatchedFixture(t)

	downloadDir := t.TempDir()
	writeEpubAt(t, filepath.Join(downloadDir, "Some Unknown Book.epub"), "Some Unknown Book", "Nobody", "")

	dl := &models.Download{GUID: "guid-nomatch", Title: "Some Unknown Book", BookID: nil, Status: models.StateCompleted, NZBURL: "fake://url"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	reloaded, err := downloads.GetByGUID(ctx, "guid-nomatch")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.BookID != nil {
		t.Errorf("BookID = %v, want nil (must not guess a match)", *reloaded.BookID)
	}
	if reloaded.Status != models.StateImportFailed {
		t.Errorf("status = %q, want %q", reloaded.Status, models.StateImportFailed)
	}
	// #1589: the files' location is recorded so a later manual "Match to book"
	// can import them directly rather than hunting for the path.
	if reloaded.ImportPath != downloadDir {
		t.Errorf("ImportPath = %q, want %q (recorded for manual match)", reloaded.ImportPath, downloadDir)
	}
}

// TestRecordUnmatchedImportPath_EmptyPathNoOp covers the guard: an empty path is
// never persisted (nothing to import from), so the column stays clear.
func TestRecordUnmatchedImportPath_EmptyPathNoOp(t *testing.T) {
	s, downloads, _, _, _, _, ctx := unmatchedFixture(t)
	dl := &models.Download{GUID: "empty-path", Title: "t", NZBURL: "x", Status: models.StateImportFailed}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	s.recordUnmatchedImportPath(ctx, dl.ID, "")
	got, _ := downloads.GetByID(ctx, dl.ID)
	if got.ImportPath != "" {
		t.Errorf("ImportPath = %q, want empty (empty path is a no-op)", got.ImportPath)
	}
}

// TestRecordUnmatchedImportPath_WriteErrorLogged covers the error branch: a
// failing SetImportPath (here a read-only DB) is logged and swallowed — recording
// the path is best-effort and must never abort the import failure handling.
func TestRecordUnmatchedImportPath_WriteErrorLogged(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	downloads := db.NewDownloadRepo(database)
	dl := &models.Download{GUID: "roerr", Title: "t", NZBURL: "x", Status: models.StateImportFailed}
	if err := downloads.Create(context.Background(), dl); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(downloads, db.NewDownloadClientRepo(database), db.NewBookRepo(database),
		db.NewAuthorRepo(database), db.NewHistoryRepo(database), t.TempDir(), "", "", "", "")

	s.recordUnmatchedImportPath(context.Background(), dl.ID, "/data/downloads/whatever") // UPDATE fails, logged

	got, _ := downloads.GetByID(context.Background(), dl.ID)
	if got.ImportPath != "" {
		t.Errorf("ImportPath = %q, want empty (write failed, logged)", got.ImportPath)
	}
}
