package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	profiles *db.MetadataProfileRepo
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
		profiles: db.NewMetadataProfileRepo(database),
		ctx:      ctx,
	}
	authors := db.NewAuthorRepo(database)
	f.s = NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		f.books, authors, f.history, libDir, "", "", "", "").
		WithSeriesRepo(f.series).
		WithSettings(f.settings).
		WithLanguageEnforcement(f.profiles, db.NewBlocklistRepo(database))
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

// writeEpubWithLanguages writes a book sized EPUB at dst declaring each of
// langs as a dc:language, in order.
func writeEpubWithLanguages(t *testing.T, dst string, langs ...string) {
	t.Helper()
	var decl strings.Builder
	for _, l := range langs {
		decl.WriteString("<dc:language>" + l + "</dc:language>")
	}
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Recursion</dc:title><dc:creator>Blake Crouch</dc:creator>` + decl.String() + `
  </metadata>
</package>`
	data, err := os.ReadFile(writeTestEpub(t, "content.opf", opf))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFillLanguageFromFile_StaleSnapshot is the long scan case: the scan read
// the book blank and unlocked at its start, and by the time it reaches the
// file a provider set a language, or the user locked the field. The write must
// see the row as it is now, not as the scan listed it.
func TestFillLanguageFromFile_StaleSnapshot(t *testing.T) {
	libDir := t.TempDir()
	p := filepath.Join(libDir, "Recursion.epub")
	writeEpubWithLanguage(t, p, "fr")

	t.Run("language set since", func(t *testing.T) {
		f := newFileLanguageFixture(t, libDir, "Blake Crouch")
		book := f.addBook(t, &models.Book{ForeignID: "ol:stale-a", Title: "Recursion"})
		stale := f.reload(t, book.ID)
		if err := f.books.SetLanguage(f.ctx, book.ID, "eng"); err != nil {
			t.Fatal(err)
		}
		if filled, _ := f.s.fillLanguageFromFile(f.ctx, stale, f.author, p); filled {
			t.Error("reported a fill over a language set since the snapshot")
		}
		if got := f.reload(t, book.ID).Language; got != "eng" {
			t.Errorf("language = %q, want eng kept", got)
		}
	})

	t.Run("locked since", func(t *testing.T) {
		f := newFileLanguageFixture(t, libDir, "Blake Crouch")
		book := f.addBook(t, &models.Book{ForeignID: "ol:stale-b", Title: "Recursion"})
		stale := f.reload(t, book.ID)
		locked := f.reload(t, book.ID)
		locked.LockField(models.BookFieldLanguage)
		if err := f.books.Update(f.ctx, locked); err != nil {
			t.Fatal(err)
		}
		if filled, _ := f.s.fillLanguageFromFile(f.ctx, stale, f.author, p); filled {
			t.Error("reported a fill over a lock added since the snapshot")
		}
		if got := f.reload(t, book.ID).Language; got != "" {
			t.Errorf("language = %q, want the locked empty value kept", got)
		}
	})
}

// TestScanLibrary_RestrictedProfilePicksAllowedLanguage: a bilingual file
// records the first declared language the author's profile allows, as an
// import does, and the first declared one when the profile allows any.
func TestScanLibrary_RestrictedProfilePicksAllowedLanguage(t *testing.T) {
	for _, c := range []struct {
		name    string
		allowed string
		want    string
	}{
		{"english only profile", "eng", "eng"},
		{"any language profile", "", "fre"},
	} {
		t.Run(c.name, func(t *testing.T) {
			libDir := t.TempDir()
			dir := filepath.Join(libDir, "Blake Crouch", "Recursion")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeEpubWithLanguages(t, filepath.Join(dir, "Recursion - Blake Crouch.epub"), "fr", "en")
			f := newFileLanguageFixture(t, libDir, "Blake Crouch")
			p, err := f.profiles.GetByID(f.ctx, models.DefaultMetadataProfileID)
			if err != nil || p == nil {
				t.Fatalf("default profile: %v", err)
			}
			p.AllowedLanguages = c.allowed
			if err := f.profiles.Update(f.ctx, p); err != nil {
				t.Fatal(err)
			}
			book := f.addBook(t, &models.Book{ForeignID: "ol:recursion", Title: "Recursion"})

			f.s.ScanLibrary(f.ctx)

			if got := f.reload(t, book.ID).Language; got != c.want {
				t.Errorf("language = %q, want %q", got, c.want)
			}
		})
	}
}

// TestBackfillFileLanguagesOnce_InterruptedWritesNoMarker: a pass cut short by
// shutdown leaves the marker unset, so the next start runs it.
func TestBackfillFileLanguagesOnce_InterruptedWritesNoMarker(t *testing.T) {
	libDir := t.TempDir()
	f := newFileLanguageFixture(t, libDir, "Blake Crouch")
	book := f.addBook(t, &models.Book{ForeignID: "ol:int", Title: "Interrupted"})
	p := filepath.Join(libDir, "Interrupted.epub")
	writeEpubWithLanguage(t, p, "es")
	if err := f.books.AddBookFile(f.ctx, book.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	f.s.BackfillFileLanguagesOnce(cancelled)
	if st, _ := f.settings.Get(f.ctx, fileLanguageBackfillKey); st != nil {
		t.Fatalf("an interrupted pass wrote the marker %q", st.Value)
	}
	if got := f.reload(t, book.ID).Language; got != "" {
		t.Fatalf("an interrupted pass filled the language (%q)", got)
	}

	f.s.BackfillFileLanguagesOnce(f.ctx)
	if got := f.reload(t, book.ID).Language; got != "spa" {
		t.Errorf("language after the next start = %q, want spa", got)
	}
	if st, _ := f.settings.Get(f.ctx, fileLanguageBackfillKey); st == nil || st.Value != "1" {
		t.Errorf("marker after a full pass = %+v, want 1", st)
	}
}

// TestBackfillFileLanguagesOnce_NothingReadableWritesNoMarker: books to fill
// and not one file that opens is a library volume not mounted yet, so the
// marker stays unset and the next start tries again.
func TestBackfillFileLanguagesOnce_NothingReadableWritesNoMarker(t *testing.T) {
	libDir := t.TempDir()
	f := newFileLanguageFixture(t, libDir, "Blake Crouch")
	var paths []string
	var books []*models.Book
	for _, title := range []string{"One", "Two"} {
		b := f.addBook(t, &models.Book{ForeignID: "ol:" + title, Title: title})
		p := filepath.Join(libDir, title+".epub")
		if err := f.books.AddBookFile(f.ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		books, paths = append(books, b), append(paths, p)
	}

	f.s.BackfillFileLanguagesOnce(f.ctx)
	if st, _ := f.settings.Get(f.ctx, fileLanguageBackfillKey); st != nil {
		t.Fatalf("a pass that opened no file wrote the marker %q", st.Value)
	}

	// The volume comes up.
	for _, p := range paths {
		writeEpubWithLanguage(t, p, "it")
	}
	f.s.BackfillFileLanguagesOnce(f.ctx)
	for _, b := range books {
		if got := f.reload(t, b.ID).Language; got != "ita" {
			t.Errorf("%s: language = %q, want ita", b.Title, got)
		}
	}
	if st, _ := f.settings.Get(f.ctx, fileLanguageBackfillKey); st == nil || st.Value != "1" {
		t.Errorf("marker = %+v, want 1", st)
	}
}
