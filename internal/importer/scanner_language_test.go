package importer

import (
	"context"
	"encoding/json"
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

// languageFixture wires an author + ebook book + a completed download pointing
// at it, and returns the scanner, download, book repo, history repo and ctx.
func languageFixture(t *testing.T, libraryDir string, book *models.Book) (*Scanner, *models.Download, *db.BookRepo, *db.HistoryRepo, context.Context) {
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
	return s, dl, bookRepo, histRepo, ctx
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
	s, dl, bookRepo, _, ctx := languageFixture(t, libraryDir, book)

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
	s, dl, bookRepo, hist, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "eng" {
		t.Fatalf("locked language = %q, want %q (must not be overwritten by EPUB de)", got.Language, "eng")
	}
	// Precedence is user > file > provider (#1933): the lock wins outright, so
	// there is no disagreement to report either.
	if events := correctionEvents(t, ctx, hist); len(events) != 0 {
		t.Errorf("a locked field produced %d correction event(s), want 0", len(events))
	}
}

// correctionEvents returns the bookLanguageCorrected rows recorded for the
// book, decoded into from/to pairs.
func correctionEvents(t *testing.T, ctx context.Context, hist *db.HistoryRepo) []map[string]string {
	t.Helper()
	events, err := hist.ListByType(ctx, models.HistoryEventBookLanguageCorrected)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]string, 0, len(events))
	for _, e := range events {
		var data map[string]string
		if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
			t.Fatalf("event data %q: %v", e.Data, err)
		}
		out = append(out, data)
	}
	return out
}

// TestTryImportInternal_CorrectsLanguageWhenEpubDisagrees is the #1933 fix: the
// catalogue said English (a work-level value from the provider), the file that
// actually landed is Spanish, and the file wins. Before this the embedded tag
// was only read when the catalogue field was empty, so the book displayed
// "English" for a Spanish EPUB indefinitely.
func TestTryImportInternal_CorrectsLanguageWhenEpubDisagrees(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "es")

	book := &models.Book{
		ForeignID: "OL-lang-correct", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Language: "eng",
	}
	s, dl, bookRepo, hist, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "spa" {
		t.Errorf("book language = %q, want %q (the imported file is Spanish)", got.Language, "spa")
	}

	// The correction must be visible, not silent: the language field alone
	// cannot say the catalogue was ever wrong.
	events := correctionEvents(t, ctx, hist)
	if len(events) != 1 {
		t.Fatalf("got %d correction events, want 1", len(events))
	}
	if events[0]["from"] != "eng" || events[0]["to"] != "spa" {
		t.Errorf("event data = %v, want from=eng to=spa", events[0])
	}
}

// TestTryImportInternal_NoCorrectionWhenLanguagesAgree guards against a write
// and a history row on every single import. The stored value is deliberately
// the two-letter "en" a provider supplies rather than the canonical "eng":
// the catalogue is NOT normalised on write, so comparing raw strings would see
// "en" against the EPUB's normalised "eng", call it a disagreement, and
// "correct" every English book to English on every import (#1729).
func TestTryImportInternal_NoCorrectionWhenLanguagesAgree(t *testing.T) {
	t.Parallel()

	for _, stored := range []string{"en", "eng", "en-US"} {
		t.Run(stored, func(t *testing.T) {
			t.Parallel()

			libraryDir := t.TempDir()
			downloadPath := t.TempDir()
			writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "en")

			book := &models.Book{
				ForeignID: "OL-lang-agree-" + stored, Title: "Recursion", SortTitle: "recursion",
				Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
				Language: stored,
			}
			s, dl, bookRepo, hist, ctx := languageFixture(t, libraryDir, book)

			s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

			got, err := bookRepo.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Language != stored {
				t.Errorf("book language = %q, want %q unchanged — the EPUB agrees, so nothing should be written", got.Language, stored)
			}
			if events := correctionEvents(t, ctx, hist); len(events) != 0 {
				t.Errorf("got %d correction events for an agreeing language, want 0: %v", len(events), events)
			}
		})
	}
}

// TestTryImportInternal_FillEmitsNoCorrectionEvent keeps the two cases apart.
// Filling a language the catalogue never had is the #1160 backfill and is not
// a correction — nothing disagreed, so a history row would be noise on every
// OpenLibrary import, which routinely arrives with no work-level language.
func TestTryImportInternal_FillEmitsNoCorrectionEvent(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "es")

	book := &models.Book{
		ForeignID: "OL-lang-fill", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Language: "",
	}
	s, dl, bookRepo, hist, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "spa" {
		t.Errorf("book language = %q, want %q", got.Language, "spa")
	}
	if events := correctionEvents(t, ctx, hist); len(events) != 0 {
		t.Errorf("a backfill emitted %d correction event(s), want 0", len(events))
	}
}

// TestTryImportInternal_NoLanguageWriteWhenNothingImported pins the guard on
// the reconcile: the EPUB is read while the source file still exists, which is
// before we know whether any file actually landed. A failed import must not
// rewrite the catalogue from a file that is not in the library — the user would
// be left with a book whose language describes something they do not have.
func TestTryImportInternal_NoLanguageWriteWhenNothingImported(t *testing.T) {
	t.Parallel()

	// A library root whose parent is a regular file: every MkdirAll under it
	// fails with ENOTDIR for any user, so no file can be placed.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libraryDir := filepath.Join(blocker, "library")

	downloadPath := t.TempDir()
	writeEpubWithLanguage(t, filepath.Join(downloadPath, "Recursion.epub"), "es")

	book := &models.Book{
		ForeignID: "OL-lang-noimport", Title: "Recursion", SortTitle: "recursion",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Language: "eng",
	}
	s, dl, bookRepo, hist, ctx := languageFixture(t, libraryDir, book)

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", "", nil, nil)

	got, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "eng" {
		t.Errorf("book language = %q, want %q — nothing was imported, so nothing should have been rewritten", got.Language, "eng")
	}
	if events := correctionEvents(t, ctx, hist); len(events) != 0 {
		t.Errorf("a failed import emitted %d correction event(s), want 0", len(events))
	}
}
