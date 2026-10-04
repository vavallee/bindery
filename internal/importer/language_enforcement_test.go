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

// langEnforceFixture is a scanner wired for the post-download language check
// (#2998): an author on a metadata profile with the given allowed languages,
// one Wanted ebook, and a grabbed download pointing at a folder the test fills.
type langEnforceFixture struct {
	scanner   *Scanner
	books     *db.BookRepo
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	history   *db.HistoryRepo
	settings  *db.SettingsRepo
	notif     *spyNotifier
	book      *models.Book
	dl        *models.Download
	dir       string
	library   string
}

type langEnforceOpts struct {
	allowed   string // metadata profile allowed_languages CSV
	bookLang  string // the catalogue's language for the book
	locked    bool   // the user locked the book's language
	preferred string // search.preferredLanguage, "" leaves it unset
}

func newLangEnforceFixture(t *testing.T, o langEnforceOpts) *langEnforceFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	settings := db.NewSettingsRepo(database)
	profiles := db.NewMetadataProfileRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	profile := &models.MetadataProfile{Name: "Restricted", AllowedLanguages: o.allowed, UnknownLanguageBehavior: "pass"}
	if err := profiles.Create(ctx, profile); err != nil {
		t.Fatalf("create metadata profile: %v", err)
	}
	author := &models.Author{
		ForeignID: "OL-LANG-A", Name: "John Grisham", SortName: "Grisham, John",
		MetadataProvider: "openlibrary", Monitored: true, MetadataProfileID: &profile.ID,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-LANG-B", AuthorID: author.ID, Title: "The Racketeer",
		SortTitle: "racketeer", Status: models.BookStatusWanted, Monitored: true,
		AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
		Language: o.bookLang,
	}
	if o.locked {
		book.LockedFields = []string{models.BookFieldLanguage}
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}
	if o.preferred != "" {
		if err := settings.Set(ctx, "search.preferredLanguage", o.preferred); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	library := t.TempDir()
	notif := &spyNotifier{}
	s := NewScanner(downloads, clients, books, authors, history, library, "", "", "", "").
		WithLanguageEnforcement(profiles, blocklist)
	s.WithSettings(settings)
	s.WithNotifier(notif)

	dl := &models.Download{
		BookID: &book.ID, GUID: "guid-racketeer", Title: "John.Grisham.The.Racketeer.EPUB",
		Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return &langEnforceFixture{
		scanner: s, books: books, downloads: downloads, blocklist: blocklist,
		history: history, settings: settings, notif: notif, book: book, dl: dl,
		dir: dir, library: library,
	}
}

// writeEpub drops an EPUB declaring the given dc:language ("" declares none)
// into the download folder.
func (f *langEnforceFixture) writeEpub(t *testing.T, language string) {
	t.Helper()
	writeEpubWithLanguage(t, filepath.Join(f.dir, "The Racketeer.epub"), language)
}

// importDownload runs the import the way a download client poll does.
func (f *langEnforceFixture) importDownload(t *testing.T) {
	t.Helper()
	f.scanner.tryImportInternal(context.Background(), f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)
}

func (f *langEnforceFixture) reload(t *testing.T) (*models.Download, *models.Book) {
	t.Helper()
	ctx := context.Background()
	dl, err := f.downloads.GetByID(ctx, f.dl.ID)
	if err != nil || dl == nil {
		t.Fatalf("reload download: %v", err)
	}
	book, err := f.books.GetByID(ctx, f.book.ID)
	if err != nil || book == nil {
		t.Fatalf("reload book: %v", err)
	}
	return dl, book
}

func (f *langEnforceFixture) bookFiles(t *testing.T) []models.BookFile {
	t.Helper()
	files, err := f.books.ListFiles(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (f *langEnforceFixture) blocked(t *testing.T) bool {
	t.Helper()
	ok, err := f.blocklist.IsBlocked(context.Background(), f.dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// assertImported checks the download landed in the library as today.
func (f *langEnforceFixture) assertImported(t *testing.T) *models.Book {
	t.Helper()
	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	if len(f.bookFiles(t)) == 0 {
		t.Fatal("no book file recorded for an import that should have landed")
	}
	if f.blocked(t) {
		t.Error("an allowed release was blocklisted")
	}
	return book
}

// TestLanguageEnforcement_RejectsDisallowedLanguage is the #2998 report: an
// English only profile, a release whose name does not say its language, and a
// Swedish EPUB inside it. Before the fix the book was relabelled Swedish and
// the file kept; now the release is rejected like any other wrong release.
func TestLanguageEnforcement_RejectsDisallowedLanguage(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.writeEpub(t, "sv")
	ctx := context.Background()

	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q, want %q: a Swedish file imported against an English only profile", dl.Status, models.StateImportBlocked)
	}
	for _, want := range []string{"Swedish", "English"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("rejection message %q does not name %s", dl.ErrorMessage, want)
		}
	}
	if dl.ImportPath == "" {
		t.Error("import path not recorded, so Match to book cannot force the file in")
	}
	if !f.blocked(t) {
		t.Error("release was not blocklisted, so the next search grabs it again")
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want %q so the normal search picks another release", book.Status, models.BookStatusWanted)
	}
	if book.Language != "eng" {
		t.Errorf("book language = %q, want %q: a rejected file must not relabel the book", book.Language, "eng")
	}
	if files := f.bookFiles(t); len(files) != 0 {
		t.Errorf("rejected file was recorded on the book: %+v", files)
	}
	if entries, _ := os.ReadDir(f.library); len(entries) != 0 {
		t.Errorf("rejected file was placed in the library: %d entries", len(entries))
	}
	if events := correctionEvents(t, ctx, f.history); len(events) != 0 {
		t.Errorf("got %d language correction events for a rejected file, want 0", len(events))
	}
	failed, err := f.history.ListByType(ctx, models.HistoryEventImportFailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 {
		t.Errorf("got %d importFailed history rows, want 1", len(failed))
	}
	if f.notif.lookup(notifierEventDownloadFailed) == nil {
		t.Error("no downloadFailed notification for the rejection")
	}
}

// TestLanguageEnforcement_AllowedLanguageImports covers region tags and the
// other spellings a dc:language arrives in. Each must compare equal to the
// profile's ISO 639-2/B entry.
func TestLanguageEnforcement_AllowedLanguageImports(t *testing.T) {
	cases := []struct{ allowed, declared string }{
		{"eng", "en"},
		{"eng", "en-US"},
		{"eng", "en_GB"},
		{"eng", "EN"},
		{"eng", "eng"},
		{"eng,swe", "sv-SE"},
		{"ger", "deu"},
		{"ger", "de-AT"},
		{"nor", "nob"},
		{"nor", "nb-NO"},
		{"nob", "no"},
		{"nor", "nn"},
	}
	for _, c := range cases {
		t.Run(c.allowed+"/"+c.declared, func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: c.allowed})
			f.writeEpub(t, c.declared)
			f.importDownload(t)
			f.assertImported(t)
		})
	}
}

// TestLanguageEnforcement_UndeclaredOrUnknownImports keeps a file that does not
// say what language it is in. Rejecting on ignorance would block legitimate
// books, the same reason the release name filter passes an untagged name.
func TestLanguageEnforcement_UndeclaredOrUnknownImports(t *testing.T) {
	for _, declared := range []string{"", "und", "mul", "zxx", "UND", "unknown", "x-default"} {
		t.Run("declared="+declared, func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.writeEpub(t, declared)
			f.importDownload(t)
			f.assertImported(t)
		})
	}
}

// TestLanguageEnforcement_AnyLanguageKeepsRelabelling: a profile that allows
// every language has nothing to enforce, so #1933's relabelling stays exactly
// as it was for people who keep every language.
func TestLanguageEnforcement_AnyLanguageKeepsRelabelling(t *testing.T) {
	for _, allowed := range []string{"any", ""} {
		t.Run("allowed="+allowed, func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: allowed, bookLang: "eng"})
			f.writeEpub(t, "sv")
			f.importDownload(t)
			book := f.assertImported(t)
			if book.Language != "swe" {
				t.Errorf("book language = %q, want %q (relabelled from the file)", book.Language, "swe")
			}
			if events := correctionEvents(t, context.Background(), f.history); len(events) != 1 {
				t.Errorf("got %d correction events, want 1", len(events))
			}
		})
	}
}

// TestLanguageEnforcement_PreferredLanguageFallback mirrors the grab time
// filter: with an any language profile, search.preferredLanguage "en" is what
// filtered release names, so it is what the file is held to as well.
func TestLanguageEnforcement_PreferredLanguageFallback(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "any", bookLang: "eng", preferred: "en"})
	f.writeEpub(t, "sv")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q, want %q", dl.Status, models.StateImportBlocked)
	}
	if !f.blocked(t) {
		t.Error("release was not blocklisted")
	}
	if book.Language != "eng" {
		t.Errorf("book language = %q, want eng", book.Language)
	}
}

// TestLanguageEnforcement_PreferredAnyDoesNotEnforce: "any" (or anything but
// "en", which is all the grab filter acts on) enforces nothing.
func TestLanguageEnforcement_PreferredAnyDoesNotEnforce(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "any", bookLang: "eng", preferred: "any"})
	f.writeEpub(t, "sv")
	f.importDownload(t)
	f.assertImported(t)
}

// TestLanguageEnforcement_ManualImportNotRejected: a manual import is the user
// choosing this file, so it is never refused for its language. That includes
// the queue's Match to book, which is how a rejected release is forced in.
func TestLanguageEnforcement_ManualImportNotRejected(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.writeEpub(t, "sv")

	f.scanner.ImportFromPath(context.Background(), f.dl, f.dir, "")

	f.assertImported(t)
}

// TestLanguageEnforcement_ManualMultiFileImportNotRejected covers the other
// manual entry point (#2935).
func TestLanguageEnforcement_ManualMultiFileImportNotRejected(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.writeEpub(t, "sv")

	f.scanner.ImportFilesFromPath(context.Background(), f.dl, f.dir,
		[]string{filepath.Join(f.dir, "The Racketeer.epub")}, models.MediaTypeEbook)

	f.assertImported(t)
}

// TestLanguageEnforcement_LockedLanguageIsAllowed: a user who locked a book's
// language to Swedish has said this book is Swedish, so a Swedish file for it
// is wanted even when the author's profile is English only.
func TestLanguageEnforcement_LockedLanguageIsAllowed(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "swe", locked: true})
	f.writeEpub(t, "sv")
	f.importDownload(t)
	book := f.assertImported(t)
	if book.Language != "swe" {
		t.Errorf("locked language = %q, want swe", book.Language)
	}
}

// TestLanguageEnforcement_LockedLanguageDoesNotWidenToOthers: the lock adds
// its own language and nothing else. A German file for a book locked to
// Swedish under an English only profile is still the wrong release.
func TestLanguageEnforcement_LockedLanguageDoesNotWidenToOthers(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "swe", locked: true})
	f.writeEpub(t, "de")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q, want %q", dl.Status, models.StateImportBlocked)
	}
	if book.Language != "swe" {
		t.Errorf("locked language = %q, want swe", book.Language)
	}
}

// TestLanguageEnforcement_UnwiredScannerKeepsRelabelling: without the wiring
// the check does not run, so every existing caller behaves as before.
func TestLanguageEnforcement_UnwiredScannerKeepsRelabelling(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.scanner.metadataProfiles = nil
	f.writeEpub(t, "sv")
	f.importDownload(t)
	book := f.assertImported(t)
	if book.Language != "swe" {
		t.Errorf("book language = %q, want swe (relabelled, as before #2998)", book.Language)
	}
}

// writeEpubDeclaring writes an EPUB named name into the download folder that
// declares each of langs as its own dc:language, in order.
func (f *langEnforceFixture) writeEpubDeclaring(t *testing.T, name string, langs ...string) string {
	t.Helper()
	var decl strings.Builder
	for _, l := range langs {
		decl.WriteString("    <dc:language>" + l + "</dc:language>\n")
	}
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>The Racketeer</dc:title>
    <dc:creator>John Grisham</dc:creator>
` + decl.String() + `  </metadata>
</package>`
	src := writeTestEpub(t, "content.opf", opf)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(f.dir, name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// placedLanguages reads back the languages of every EPUB recorded on the book.
func (f *langEnforceFixture) placedLanguages(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	for _, bf := range f.bookFiles(t) {
		meta, err := ReadEpubMetadata(bf.Path)
		if err != nil {
			t.Fatalf("read placed file %s: %v", bf.Path, err)
		}
		out = append(out, meta.Languages)
	}
	return out
}

// TestLanguageEnforcement_EveryDeclaredLanguageCounts: an EPUB may declare
// several languages (a bilingual edition, or a stray tag ahead of the real
// one). It is allowed when any of them is, whatever order they come in. Only
// the first used to be read, so "fr, en" was blocked while "en, fr" imported.
func TestLanguageEnforcement_EveryDeclaredLanguageCounts(t *testing.T) {
	for _, order := range [][]string{{"fr", "en"}, {"en", "fr"}, {"und", "en-GB"}} {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.writeEpubDeclaring(t, "The Racketeer.epub", order...)
			f.importDownload(t)
			book := f.assertImported(t)
			// The relabelling must not pick the disallowed language either.
			if book.Language != "eng" {
				t.Errorf("book language = %q, want eng (the allowed declared language)", book.Language)
			}
		})
	}
}

// TestLanguageEnforcement_MixedReleaseImportsAllowedFile: a release carrying a
// Swedish EPUB and an English one is not the wrong release, it holds the right
// file too. Only the first EPUB used to decide, so "A sv, B en" was rejected
// while "A en, B sv" imported. Now the English file is imported and the
// Swedish one skipped, in either order, the way the format check skips a
// disallowed format inside a mixed release.
func TestLanguageEnforcement_MixedReleaseImportsAllowedFile(t *testing.T) {
	for _, swedishFirst := range []bool{true, false} {
		name := "english-first"
		if swedishFirst {
			name = "swedish-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			sv, en := "A.epub", "B.epub"
			if !swedishFirst {
				sv, en = "B.epub", "A.epub"
			}
			f.writeEpubDeclaring(t, sv, "sv")
			f.writeEpubDeclaring(t, en, "en")
			f.importDownload(t)
			book := f.assertImported(t)
			placed := f.placedLanguages(t)
			if len(placed) != 1 || len(placed[0]) != 1 || placed[0][0] != "eng" {
				t.Errorf("placed files declare %v, want exactly one English file", placed)
			}
			if book.Language != "eng" {
				t.Errorf("book language = %q, want eng", book.Language)
			}
		})
	}
}

// TestLanguageEnforcement_AllFilesDisallowedRejects: several EPUBs, none in an
// allowed language, is still the wrong release.
func TestLanguageEnforcement_AllFilesDisallowedRejects(t *testing.T) {
	f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
	f.writeEpubDeclaring(t, "A.epub", "sv")
	f.writeEpubDeclaring(t, "B.epub", "de", "und")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q, want %q", dl.Status, models.StateImportBlocked)
	}
	for _, want := range []string{"Swedish", "German", "English"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("rejection message %q does not name %s", dl.ErrorMessage, want)
		}
	}
	if !f.blocked(t) {
		t.Error("release was not blocklisted")
	}
	if book.Language != "eng" {
		t.Errorf("book language = %q, want eng", book.Language)
	}
}

// TestLanguageEnforcement_TwoLetterCodesOutsideTheOldTable: Ukrainian, Hebrew
// and Slovak EPUBs tag themselves "uk", "he", "sk". Those codes were missing
// from the two letter table, so they normalised to nothing Bindery knew,
// counted as "no language declared", imported under an English only profile
// and relabelled the book.
func TestLanguageEnforcement_TwoLetterCodesOutsideTheOldTable(t *testing.T) {
	for _, c := range []struct{ code, name string }{
		{"uk", "Ukrainian"}, {"he", "Hebrew"}, {"sk", "Slovak"}, {"fa-IR", "Persian"}, {"is", "Icelandic"},
	} {
		t.Run(c.code, func(t *testing.T) {
			f := newLangEnforceFixture(t, langEnforceOpts{allowed: "eng", bookLang: "eng"})
			f.writeEpub(t, c.code)
			f.importDownload(t)
			dl, book := f.reload(t)
			if dl.Status != models.StateImportBlocked {
				t.Fatalf("download status = %q, want %q: a %s file imported against an English only profile", dl.Status, models.StateImportBlocked, c.name)
			}
			if !strings.Contains(dl.ErrorMessage, c.name) {
				t.Errorf("rejection message %q does not name %s", dl.ErrorMessage, c.name)
			}
			if book.Language != "eng" {
				t.Errorf("book language = %q, want eng (not relabelled)", book.Language)
			}
		})
	}
}
