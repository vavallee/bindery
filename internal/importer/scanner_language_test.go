package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// writeEpubWithLanguage builds a minimal EPUB with the given dc:language and writes it at
// dst (so it can live inside a download folder rather than its own temp dir).
func writeEpubWithLanguage(t *testing.T, dst, language string) {
	t.Helper()
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Recursion</dc:title>
    <dc:creator>Blake Crouch</dc:creator>
    <dc:language>` + language + `</dc:language>
  </metadata>
</package>`
	src := writeTestEpub(t, "content.opf", opf)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// languageFixture wires an author + ebook book (empty language) + a completed
// download pointing at it, and returns the scanner, download, book repo and ctx.
func languageFixture(t *testing.T, libraryDir string, book *models.Book) (*Scanner, *models.Download, *db.BookRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	author := &models.Author{ForeignID: "OL-lang-test", Name: "Blake Crouch", SortName: "Crouch, Blake"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book.AuthorID = author.ID
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{
		GUID:   "guid-lang-test",
		Title:  book.Title,
		BookID: &book.ID,
		Status: models.StateCompleted,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return s, dl, bookRepo, ctx
}

// TestTryImportInternal_FillsLanguageFromEpub verifies the #1160 import-time
// backfill: an ebook with an empty catalogue language takes the normalised
// dc:language ("en" -> "eng") from the imported EPUB.
func TestTryImportInternal_FillsLanguageFromEpub(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "en")

	book := &models.Book{
		ForeignID: "OL-lang-book", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, Language: "",
	}
	s, dl, bookRepo, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "eng" {
		t.Fatalf("book language = %q, want %q (backfilled from EPUB dc:language)", got.Language, "eng")
	}
}

// TestTryImportInternal_DoesNotOverwriteLockedLanguage verifies the backfill
// respects a user-locked language field (#1446): a locked value survives an
// import whose EPUB reports a different language.
func TestTryImportInternal_DoesNotOverwriteLockedLanguage(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "de")

	book := &models.Book{
		ForeignID: "OL-lang-locked", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Language:     "eng",
		LockedFields: []string{models.BookFieldLanguage},
	}
	s, dl, bookRepo, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "eng" {
		t.Fatalf("locked language = %q, want %q (must not be overwritten by EPUB de)", got.Language, "eng")
	}
}
