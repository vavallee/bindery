package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// contentFixture is a scanner wired for the ebook content check (#3015): the
// author James Patterson, one Wanted ebook "Mary, Mary: Alex Cross, Book 11",
// and a grabbed download pointing at a folder the test fills.
type contentFixture struct {
	scanner   *Scanner
	books     *db.BookRepo
	authors   *db.AuthorRepo
	aliases   *db.AuthorAliasRepo
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	history   *db.HistoryRepo
	settings  *db.SettingsRepo
	notif     *spyNotifier
	author    *models.Author
	book      *models.Book
	dl        *models.Download
	dir       string
	library   string
	checks    []string
	sqlDB     *sql.DB
}

func newContentFixture(t *testing.T, bookTitle, authorName string) *contentFixture {
	t.Helper()
	return newContentFixtureFor(t, bookTitle, authorName, models.MediaTypeEbook)
}

// newContentFixtureFor is newContentFixture for a book of the given media type.
func newContentFixtureFor(t *testing.T, bookTitle, authorName, mediaType string) *contentFixture {
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
	aliases := db.NewAuthorAliasRepo(database)

	author := &models.Author{
		ForeignID: "OL-CC-A", Name: authorName, SortName: authorName,
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-CC-B", AuthorID: author.ID, Title: bookTitle,
		SortTitle: strings.ToLower(bookTitle), Status: models.BookStatusWanted, Monitored: true,
		AnyEditionOK: true, MediaType: mediaType, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	f := &contentFixture{
		sqlDB: database, books: books, authors: authors, aliases: aliases, downloads: downloads,
		blocklist: blocklist, history: history, settings: settings,
		notif: &spyNotifier{}, author: author, book: book,
		dir: t.TempDir(), library: t.TempDir(),
	}
	f.scanner = NewScanner(downloads, clients, books, authors, history, f.library, "", "", "", "").
		WithLanguageEnforcement(profiles, blocklist).
		WithAuthorAliases(aliases)
	f.scanner.WithSettings(settings)
	f.scanner.WithNotifier(f.notif)
	f.scanner.onContentCheck = func(kind string) { f.checks = append(f.checks, kind) }

	f.dl = &models.Download{
		BookID: &book.ID, GUID: "guid-content", Title: "Release.EPUB",
		Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := downloads.Create(ctx, f.dl); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *contentFixture) setMode(t *testing.T, key, value string) {
	t.Helper()
	if err := f.settings.Set(context.Background(), key, value); err != nil {
		t.Fatal(err)
	}
}

// writeEpubTitled drops an EPUB whose OPF declares the given title and creator
// ("" omits that element, which is how a file with no metadata looks).
func (f *contentFixture) writeEpubTitled(t *testing.T, name, title, creator string) {
	t.Helper()
	var meta strings.Builder
	if title != "" {
		meta.WriteString("<dc:title>" + title + "</dc:title>\n")
	}
	if creator != "" {
		meta.WriteString("<dc:creator>" + creator + "</dc:creator>\n")
	}
	meta.WriteString("<dc:language>en</dc:language>\n")
	opf := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
` + meta.String() + `  </metadata>
</package>`
	src := writeTestEpub(t, "content.opf", opf)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *contentFixture) importDownload(t *testing.T) {
	t.Helper()
	f.scanner.tryImportInternal(context.Background(), f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)
}

func (f *contentFixture) reload(t *testing.T) (*models.Download, *models.Book) {
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

func (f *contentFixture) bookFileCount(t *testing.T) int {
	t.Helper()
	files, err := f.books.ListFiles(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func (f *contentFixture) blocklisted(t *testing.T) bool {
	t.Helper()
	ok, err := f.blocklist.IsBlocked(context.Background(), f.dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// eventData returns the decoded data of the newest history event of a type.
func (f *contentFixture) eventData(t *testing.T, eventType string) map[string]any {
	t.Helper()
	events, err := f.history.ListByType(context.Background(), eventType)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatalf("no %s history event", eventType)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("decode %s data %q: %v", eventType, events[0].Data, err)
	}
	return data
}

func (f *contentFixture) assertImportedSilently(t *testing.T) {
	t.Helper()
	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want imported", book.Status)
	}
	if f.blocklisted(t) {
		t.Error("an imported release was blocklisted")
	}
	if _, has := f.eventData(t, models.HistoryEventBookImported)["contentCheck"]; has {
		t.Error("a clean import carried contentCheck data")
	}
	if call := f.notif.lookup("bookImported"); call != nil {
		if _, has := call.payload["contentCheck"]; has {
			t.Error("a clean import webhook carried contentCheck")
		}
	}
}

const ebookCheckKey = "import.ebook.content_check"

// The #3015 report: the file is Mary and Mr Eliot by Mary Trevelyan, the book
// is Mary, Mary by James Patterson. In block mode nothing is placed, the release
// is blocklisted, and the book stays Wanted.
func TestEbookContentCheck_BlockRefusesAMismatchedFile(t *testing.T) {
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want %q", dl.Status, models.StateImportBlocked)
	}
	for _, want := range []string{"Mary and Mr Eliot", "Mary Trevelyan", "Mary, Mary", "James Patterson", "Match to book"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("message %q does not name %q", dl.ErrorMessage, want)
		}
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want wanted", book.Status)
	}
	if n := f.bookFileCount(t); n != 0 {
		t.Errorf("%d book file(s) recorded for a refused download", n)
	}
	if !f.blocklisted(t) {
		t.Error("the release was not blocklisted")
	}
	if dl.ImportPath == "" {
		t.Error("the download path was not recorded, so Match to book could not find the files")
	}
	cc, ok := f.eventData(t, models.HistoryEventImportFailed)["contentCheck"].(map[string]any)
	if !ok {
		t.Fatalf("importFailed event has no contentCheck object: %v", f.eventData(t, models.HistoryEventImportFailed))
	}
	if cc["kind"] != "ebook" {
		t.Errorf("contentCheck.kind = %v, want ebook", cc["kind"])
	}
	call := f.notif.lookup("downloadFailed")
	if call == nil {
		t.Fatal("no downloadFailed webhook")
	}
	if _, has := call.payload["contentCheck"]; !has {
		t.Error("the failure webhook did not carry contentCheck")
	}
}

// A file's title and author are untrusted text, and the failure message goes to
// chat webhooks as is. Mentions, Slack escapes, markdown links and bare URLs in
// them must not survive into the message, the History row or the webhook.
func TestEbookContentCheck_FileTextIsNeutralisedBeforeItIsSent(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "@everyone &lt;!channel&gt; [click](https://evil.example/x)", "https://evil.example/a Eve")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want blocked", dl.Status)
	}
	call := f.notif.lookup("downloadFailed")
	if call == nil {
		t.Fatal("no downloadFailed webhook")
	}
	blob, err := json.Marshal(call.payload)
	if err != nil {
		t.Fatal(err)
	}
	events, err := f.history.ListByType(context.Background(), models.HistoryEventImportFailed)
	if err != nil || len(events) == 0 {
		t.Fatalf("no importFailed event: %v", err)
	}
	for name, text := range map[string]string{"message": dl.ErrorMessage, "webhook": string(blob), "history": events[0].Data} {
		for _, bad := range []string{"@everyone", "<!channel>", "](https://", "://"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s still contains %q: %s", name, bad, text)
			}
		}
	}
}

// Warn is the default: nothing set means warn. The import proceeds and the event
// and the webhook say what was found.
func TestEbookContentCheck_WarnIsTheDefaultAndImports(t *testing.T) {
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if f.blocklisted(t) {
		t.Error("warn mode blocklisted the release")
	}
	cc, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
	if !ok {
		t.Fatalf("bookImported event has no contentCheck: %v", f.eventData(t, models.HistoryEventBookImported))
	}
	if cc["kind"] != "ebook" || cc["bookTitle"] != "Mary, Mary: Alex Cross, Book 11" {
		t.Errorf("contentCheck = %v", cc)
	}
	mm, _ := cc["mismatches"].([]any)
	if len(mm) != 1 {
		t.Fatalf("mismatches = %v, want one file", cc["mismatches"])
	}
	first, _ := mm[0].(map[string]any)
	if first["title"] != "Mary and Mr Eliot: A Sort of Love Story" || first["author"] != "Mary Trevelyan" {
		t.Errorf("mismatch = %v", first)
	}
	call := f.notif.lookup("bookImported")
	if call == nil {
		t.Fatal("no bookImported webhook")
	}
	if _, has := call.payload["contentCheck"]; !has {
		t.Error("the import webhook did not carry contentCheck")
	}
}

func TestEbookContentCheck_ExplicitWarnMatchesTheDefault(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "warn")
	f.writeEpubTitled(t, "book.epub", "Recursion", "Blake Crouch")
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q, want imported", dl.Status)
	}
	if _, has := f.eventData(t, models.HistoryEventBookImported)["contentCheck"]; !has {
		t.Error("warn mode attached no contentCheck")
	}
}

// Off does nothing: it does not even look.
func TestEbookContentCheck_OffImportsAndNeverRuns(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "off")
	f.writeEpubTitled(t, "book.epub", "Recursion", "Blake Crouch")
	f.importDownload(t)
	f.assertImportedSilently(t)
	if len(f.checks) != 0 {
		t.Errorf("the check ran %v with the setting off", f.checks)
	}
}

func TestEbookContentCheck_UnknownValueFallsBackToWarn(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "reject")
	f.writeEpubTitled(t, "book.epub", "Recursion", "Blake Crouch")
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("a typo in the setting blocked an import: status %q", dl.Status)
	}
}

// Both title and author must disagree. These all import in block mode.
func TestEbookContentCheck_BlockImportsWhenOneSideAgrees(t *testing.T) {
	cases := []struct {
		name, fileTitle, fileAuthor string
	}{
		{"series suffix on the book, title agrees", "Mary, Mary", "Somebody Else"},
		{"title differs, author agrees", "Cross", "James Patterson"},
		{"title differs, author listed last first", "Cross", "Patterson, James"},
		{"empty metadata", "", ""},
		{"title only", "Cross", ""},
		{"author only, other", "", "Somebody Else"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
			f.setMode(t, ebookCheckKey, "block")
			f.writeEpubTitled(t, "book.epub", c.fileTitle, c.fileAuthor)
			f.importDownload(t)
			f.assertImportedSilently(t)
		})
	}
}

// "Franklin W. Dixon" is a house name; the catalogue says Leslie McFarlane. The
// alias makes the author side agree, so a different looking title is fine.
func TestEbookContentCheck_AliasMatchesAPenName(t *testing.T) {
	f := newContentFixture(t, "The Tower Treasure", "Leslie McFarlane")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Hardy Boys 01", "Franklin W. Dixon")
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("without the alias the pen name is a different author: status %q, want blocked", dl.Status)
	}

	f2 := newContentFixture(t, "The Tower Treasure", "Leslie McFarlane")
	f2.setMode(t, ebookCheckKey, "block")
	if err := f2.aliases.Create(context.Background(), &models.AuthorAlias{AuthorID: f2.author.ID, Name: "Franklin W. Dixon"}); err != nil {
		t.Fatal(err)
	}
	f2.writeEpubTitled(t, "book.epub", "Hardy Boys 01", "Franklin W. Dixon")
	f2.importDownload(t)
	f2.assertImportedSilently(t)
}

// Match to book (a manual import) is the override and skips the check.
func TestEbookContentCheck_MatchToBookSkipsTheCheck(t *testing.T) {
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("setup: status = %q, want blocked", dl.Status)
	}
	f.checks = nil

	f.scanner.ImportFromPath(context.Background(), f.dl, f.dir, "")
	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("Match to book did not import: status %q (%s)", dl.Status, dl.ErrorMessage)
	}
	if book.Status != models.BookStatusImported || f.bookFileCount(t) == 0 {
		t.Error("the overridden import did not attach the file")
	}
	if len(f.checks) != 0 {
		t.Errorf("the check ran %v on a manual import", f.checks)
	}
}

// Retry import goes back through the poll path, so it runs the check again and
// reads the setting afresh.
func TestEbookContentCheck_RetryImportReRunsTheCheck(t *testing.T) {
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("setup: status = %q, want blocked", dl.Status)
	}

	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("retry with the setting unchanged: status = %q, want blocked again", dl.Status)
	}
	if len(f.checks) != 2 {
		t.Errorf("the check ran %d times over two attempts, want 2", len(f.checks))
	}

	f.setMode(t, ebookCheckKey, "warn")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("retry after switching to warn: status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
}

// Only EPUB is read. A PDF or MOBI is imported as before, in every mode.
func TestEbookContentCheck_OnlyEpubIsChecked(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	if err := os.WriteFile(filepath.Join(f.dir, "The Racketeer.pdf"), []byte(strings.Repeat("%PDF-1.4 ", 6000)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	f.assertImportedSilently(t)
}

// A release that holds one good EPUB and one stray mismatched one is not
// refused: only a release whose every readable EPUB disagrees is.
func TestEbookContentCheck_OneAgreeingEpubSavesTheRelease(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "a.epub", "Recursion", "Blake Crouch")
	f.writeEpubTitled(t, "b.epub", "The Racketeer", "John Grisham")
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
}

func TestEbookContentCheck_EveryEpubDisagreeingBlocks(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "a.epub", "Recursion", "Blake Crouch")
	f.writeEpubTitled(t, "b.epub", "Dark Matter", "Blake Crouch")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want blocked", dl.Status)
	}
}

// A wanted box set or omnibus is not compared: its file legitimately carries
// some other title.
func TestEbookContentCheck_BoxSetBookIsNotChecked(t *testing.T) {
	f := newContentFixture(t, "Alex Cross Box Set", "James Patterson")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Along Came a Spider", "Someone Else")
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if len(f.checks) != 0 {
		t.Errorf("the check ran for a box set: %v", f.checks)
	}
}

// The check is for downloads. A library scan never runs it, and a polled
// download does.
func TestContentCheck_NotRunByLibraryScan(t *testing.T) {
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.setMode(t, ebookCheckKey, "block")
	f.scanner.libraryDir = f.library
	f.writeEpubTitled(t, "stray.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	data, err := os.ReadFile(filepath.Join(f.dir, "stray.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.library, "Mary Trevelyan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.library, "Mary Trevelyan", "Mary and Mr Eliot.epub"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	f.scanner.ScanLibrary(context.Background())
	if len(f.checks) != 0 {
		t.Errorf("a library scan ran the content check: %v", f.checks)
	}
	if blocked, _ := f.blocklist.IsBlocked(context.Background(), f.dl.GUID); blocked {
		t.Error("a library scan blocklisted a release")
	}

	f.writeEpubTitled(t, "book.epub", "Mary, Mary", "James Patterson")
	f.importDownload(t)
	if len(f.checks) != 1 || f.checks[0] != "ebook" {
		t.Errorf("a polled download ran checks %v, want exactly one ebook check", f.checks)
	}
}

// A download with no book of its own is matched from the file, so comparing the
// file to the book it just matched on would prove nothing.
func TestEbookContentCheck_SkippedWhenTheBookWasRecoveredFromTheFile(t *testing.T) {
	f := newContentFixture(t, "The Racketeer", "John Grisham")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "The Racketeer", "John Grisham")
	if _, err := f.sqlDB.ExecContext(context.Background(), "UPDATE downloads SET book_id = NULL WHERE id = ?", f.dl.ID); err != nil {
		t.Fatal(err)
	}
	f.dl.BookID = nil
	f.importDownload(t)
	if len(f.checks) != 0 {
		t.Errorf("the check ran for a recovered association: %v", f.checks)
	}
}
