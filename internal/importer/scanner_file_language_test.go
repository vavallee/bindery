package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The library scan fills a book's empty language from the ebook it attaches
// (#1160). It fills a blank only: a provider's language and a user's locked
// edit are never replaced, unlike the import path where the file wins (#1933).

// fileLanguageFixture is a scanner over libDir with an in-memory database, a
// settings repo and a series repo, plus one author.
type fileLanguageFixture struct {
	s        *Scanner
	books    *db.BookRepo
	series   *db.SeriesRepo
	settings *db.SettingsRepo
	history  *db.HistoryRepo
	author   *models.Author
	ctx      context.Context
}

func newFileLanguageFixture(t *testing.T, libDir, authorName string) fileLanguageFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	f := fileLanguageFixture{
		books:    db.NewBookRepo(database),
		series:   db.NewSeriesRepo(database),
		settings: db.NewSettingsRepo(database),
		history:  db.NewHistoryRepo(database),
		ctx:      ctx,
	}
	authors := db.NewAuthorRepo(database)
	f.s = NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		f.books, authors, f.history, libDir, "", "", "", "").
		WithSeriesRepo(f.series).
		WithSettings(f.settings).
		WithLanguageEnforcement(db.NewMetadataProfileRepo(database), db.NewBlocklistRepo(database))
	f.author = &models.Author{ForeignID: "ol:file-lang", Name: authorName, SortName: authorName}
	if err := authors.Create(ctx, f.author); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fileLanguageFixture) addBook(t *testing.T, b *models.Book) *models.Book {
	t.Helper()
	b.AuthorID = f.author.ID
	if b.SortTitle == "" {
		b.SortTitle = b.Title
	}
	if b.Status == "" {
		b.Status = models.BookStatusWanted
	}
	b.Monitored = true
	b.Genres = []string{}
	if err := f.books.Create(f.ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (f fileLanguageFixture) reload(t *testing.T, id int64) *models.Book {
	t.Helper()
	got, err := f.books.GetByID(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f fileLanguageFixture) wantAttached(t *testing.T, bookID int64, path string) {
	t.Helper()
	files, err := f.books.ListFiles(f.ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("book %d files = %+v, want the one file %s", bookID, files, path)
	}
}

// languageCase is one starting state of the book a scan attaches a file to.
type languageCase struct {
	name     string
	language string
	locked   bool
	want     string
}

// languageCases are the three states every attach point is held to: a blank
// is filled from the file, a provider's language is kept, and a locked field
// (here locked empty, which is what clearing the language and saving does) is
// left alone.
func languageCases(fileLang string) []languageCase {
	return []languageCase{
		{name: "blank is filled", want: fileLang},
		{name: "provider language kept", language: "eng", want: "eng"},
		{name: "locked is skipped", locked: true, want: ""},
	}
}

func (c languageCase) book(b *models.Book) *models.Book {
	b.Language = c.language
	if c.locked {
		b.LockedFields = []string{models.BookFieldLanguage}
	}
	return b
}

func TestScanLibrary_TitleMatchLanguage(t *testing.T) {
	for _, c := range languageCases("fre") {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			dir := filepath.Join(libDir, "Blake Crouch", "Recursion")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "Recursion - Blake Crouch.epub")
			writeEpubWithLanguage(t, p, "fr-FR")
			f := newFileLanguageFixture(t, libDir, "Blake Crouch")
			book := f.addBook(t, c.book(&models.Book{ForeignID: "ol:recursion", Title: "Recursion"}))

			f.s.ScanLibrary(f.ctx)

			f.wantAttached(t, book.ID, p)
			if got := f.reload(t, book.ID).Language; got != c.want {
				t.Errorf("language = %q, want %q", got, c.want)
			}
			events, err := f.history.ListByType(f.ctx, models.HistoryEventBookLanguageCorrected)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Errorf("a scan wrote %d language corrected rows, want none", len(events))
			}
		})
	}
}

// TestScanLibrary_ASINMatchLanguage covers the ASIN tier: the catalogue title
// shares nothing with the file name, so only the ASIN in the name attaches it.
func TestScanLibrary_ASINMatchLanguage(t *testing.T) {
	for _, c := range languageCases("ger") {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			p := filepath.Join(libDir, "The Sparrow B01LVSUORS - Mary Doria Russell.epub")
			writeEpubWithLanguage(t, p, "de")
			if parsed := ParseFilename(p); parsed.ASIN != "B01LVSUORS" {
				t.Fatalf("fixture parse: ASIN = %q", parsed.ASIN)
			}
			f := newFileLanguageFixture(t, libDir, "Ursula North")
			book := f.addBook(t, c.book(&models.Book{ForeignID: "ol:quantum", Title: "Quantum Gardens", ASIN: "B01LVSUORS"}))

			f.s.ScanLibrary(f.ctx)

			f.wantAttached(t, book.ID, p)
			if got := f.reload(t, book.ID).Language; got != c.want {
				t.Errorf("language = %q, want %q", got, c.want)
			}
		})
	}
}

// TestScanLibrary_SeriesMatchLanguage covers the series position tier, with
// the disjoint fixture TestScanLibrary_SeriesPositionReconcileTier uses.
func TestScanLibrary_SeriesMatchLanguage(t *testing.T) {
	for _, c := range languageCases("ita") {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			p := filepath.Join(libDir, "[Stormlight Archive, Book 1] The Way of Kings - Brandon Sanderson.epub")
			writeEpubWithLanguage(t, p, "it")
			f := newFileLanguageFixture(t, libDir, "Ursula North")
			book := f.addBook(t, c.book(&models.Book{ForeignID: "ol:quantum", Title: "Quantum Gardens"}))
			ser := &models.Series{ForeignID: "manual:stormlight", Title: "Stormlight Archive"}
			if err := f.series.Create(f.ctx, ser); err != nil {
				t.Fatal(err)
			}
			if err := f.series.LinkBook(f.ctx, ser.ID, book.ID, "1", true); err != nil {
				t.Fatal(err)
			}

			f.s.ScanLibrary(f.ctx)

			f.wantAttached(t, book.ID, p)
			if got := f.reload(t, book.ID).Language; got != c.want {
				t.Errorf("language = %q, want %q", got, c.want)
			}
		})
	}
}

// TestBackfillFileLanguagesOnce covers books whose ebook was tracked before
// the scan read languages: one pass fills the blanks, leaves provider and
// locked values alone, skips a missing file, and never runs again.
func TestBackfillFileLanguagesOnce(t *testing.T) {
	libDir := t.TempDir()
	f := newFileLanguageFixture(t, libDir, "Blake Crouch")

	track := func(b *models.Book, lang string) string {
		t.Helper()
		p := filepath.Join(libDir, b.Title+".epub")
		if lang != "" {
			writeEpubWithLanguage(t, p, lang)
		}
		if err := f.books.AddBookFile(f.ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	blank := f.addBook(t, &models.Book{ForeignID: "ol:a", Title: "Blank"})
	track(blank, "es")
	provider := f.addBook(t, &models.Book{ForeignID: "ol:b", Title: "Provider", Language: "eng"})
	track(provider, "es")
	locked := f.addBook(t, &models.Book{ForeignID: "ol:c", Title: "Locked", LockedFields: []string{models.BookFieldLanguage}})
	track(locked, "es")
	missing := f.addBook(t, &models.Book{ForeignID: "ol:d", Title: "Missing"})
	track(missing, "") // tracked, but nothing on disk
	undetermined := f.addBook(t, &models.Book{ForeignID: "ol:e", Title: "Undetermined"})
	track(undetermined, "und")

	f.s.BackfillFileLanguagesOnce(f.ctx)

	for _, c := range []struct {
		book *models.Book
		want string
	}{
		{blank, "spa"},
		{provider, "eng"},
		{locked, ""},
		{missing, ""},
		{undetermined, ""},
	} {
		if got := f.reload(t, c.book.ID).Language; got != c.want {
			t.Errorf("%s: language = %q, want %q", c.book.Title, got, c.want)
		}
	}

	st, err := f.settings.Get(f.ctx, fileLanguageBackfillKey)
	if err != nil || st == nil || st.Value != "1" {
		t.Fatalf("backfill marker = %+v (err %v), want 1", st, err)
	}

	// A second start does nothing, even with a blank to fill.
	if err := f.books.SetLanguage(f.ctx, blank.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.s.BackfillFileLanguagesOnce(f.ctx)
	if got := f.reload(t, blank.ID).Language; got != "" {
		t.Errorf("second run filled the language again (%q); it must run once", got)
	}
}
