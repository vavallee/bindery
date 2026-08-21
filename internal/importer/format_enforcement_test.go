package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// palmMobiBytes is a Palm database header carrying BOOKMOBI at offset 0x3C,
// the shape of the file in the #1782 reproduction.
func palmMobiBytes() []byte {
	b := make([]byte, 600)
	copy(b[0x3C:], []byte("BOOKMOBI"))
	return b
}

func epubBytes() []byte {
	b := make([]byte, 600)
	copy(b, []byte("PK\x03\x04"))
	copy(b[30:], []byte("mimetypeapplication/epub+zip"))
	return b
}

type formatFixture struct {
	scanner   *Scanner
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	history   *db.HistoryRepo
	book      *models.Book
	dl        *models.Download
	dir       string
}

// newFormatFixture builds a scanner whose author has a quality profile that
// disallows mobi, plus a download row pointing at a directory the caller fills.
func newFormatFixture(t *testing.T) *formatFixture {
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
	profiles := db.NewQualityProfileRepo(database)
	blocklist := db.NewBlocklistRepo(database)

	profile := &models.QualityProfile{
		Name: "EPUB only",
		Items: []models.QualityItem{
			{Quality: "epub", Allowed: true},
			{Quality: "mobi", Allowed: false},
			{Quality: "pdf", Allowed: false},
		},
	}
	if err := profiles.Create(ctx, profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	author := &models.Author{
		ForeignID: "OL-FMT-A", Name: "Format Author", SortName: "Author, Format",
		MetadataProvider: "openlibrary", Monitored: true, QualityProfileID: &profile.ID,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-FMT-B", AuthorID: author.ID, Title: "Bloodline",
		SortTitle: "bloodline", Status: models.BookStatusWanted, Monitored: true,
		AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	library := t.TempDir()
	s := NewScanner(downloads, clients, books, authors, history, library, "", "", "", "").
		WithFormatEnforcement(profiles, blocklist)
	s.WithSettings(settings)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		BookID: &book.ID, GUID: "guid-bloodline", Title: "Bloodline.Cradle.Book.9",
		Status: models.StateCompleted, Protocol: "usenet",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return &formatFixture{
		scanner: s, downloads: downloads, blocklist: blocklist, history: history,
		book: book, dl: dl, dir: dir,
	}
}

func (f *formatFixture) writeFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func (f *formatFixture) reloadDownload(t *testing.T) *models.Download {
	t.Helper()
	got, err := f.downloads.GetByID(context.Background(), f.dl.ID)
	if err != nil || got == nil {
		t.Fatalf("reload download: %v", err)
	}
	return got
}

// TestImport_ExtensionlessMobiIsRejected is the #1782 reproduction end to end.
//
// The file has no extension, so detectDownloadFormat falls through to ebook and
// nothing downstream ever looked at what it actually was.
//
// It goes through the explicit-file-list path deliberately, because that is the
// only way an extensionless file reaches the importer at all: discoverBookFiles
// walks by extension and would not find it, while filterImportableFiles accepts
// any regular non-symlink file the download client names. SABnzbd supplies that
// list, and SABnzbd is what the reporter was running.
func TestImport_ExtensionlessMobiIsRejected(t *testing.T) {
	f := newFormatFixture(t)
	name := "Will Wight - Bloodline- Cradle, Book 9"
	f.writeFile(t, name, palmMobiBytes())

	f.scanner.tryImportInternal(context.Background(), f.dl, f.dir, "", "", "", nil,
		[]string{filepath.Join(f.dir, name)})

	got := f.reloadDownload(t)
	if got.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want %q; the disallowed format imported anyway", got.Status, models.StateImportBlocked)
	}
	if got.ErrorMessage == "" {
		t.Error("blocked import recorded no reason")
	}
}

// TestImport_RejectionIsVisibleThreeWays. A rejection that only shows up as an
// absence is a book that silently never completes, so the blocked state, the
// history row and the recorded import path all have to be there: the download
// stays in the queue for manual review, the event is auditable, and the queue's
// "Match to book" action can still force it in.
func TestImport_RejectionIsVisibleThreeWays(t *testing.T) {
	f := newFormatFixture(t)
	f.writeFile(t, "book.mobi", palmMobiBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, "")

	got := f.reloadDownload(t)
	if got.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want %q", got.Status, models.StateImportBlocked)
	}
	if got.ImportPath == "" {
		t.Error("import path was not recorded, so the queue cannot offer a manual override")
	}

	events, err := f.history.List(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.EventType == models.HistoryEventImportFailed {
			found = true
		}
	}
	if !found {
		t.Error("no history row was written for the rejection")
	}
}

// TestImport_RejectedReleaseIsBlocklisted is the loop guard. Without it the
// book stays wanted, the next scan finds the same release, grabs it, downloads
// it and rejects it again, forever.
func TestImport_RejectedReleaseIsBlocklisted(t *testing.T) {
	f := newFormatFixture(t)
	f.writeFile(t, "book.mobi", palmMobiBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, "")

	blocked, err := f.blocklist.IsBlocked(ctx, "guid-bloodline")
	if err != nil {
		t.Fatalf("check blocklist: %v", err)
	}
	if !blocked {
		t.Fatal("the rejected release was not blocklisted, so the next search will grab it again")
	}
}

// TestImport_AllowedFormatStillImports is the guard against the filter becoming
// a blackout.
func TestImport_AllowedFormatStillImports(t *testing.T) {
	f := newFormatFixture(t)
	f.writeFile(t, "book.epub", epubBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, "")

	got := f.reloadDownload(t)
	if got.Status == models.StateImportBlocked {
		t.Fatalf("an allowed format was blocked: %s", got.ErrorMessage)
	}
	blocked, err := f.blocklist.IsBlocked(ctx, "guid-bloodline")
	if err != nil {
		t.Fatalf("check blocklist: %v", err)
	}
	if blocked {
		t.Error("an allowed release was blocklisted")
	}
}

// TestImport_MixedDownloadImportsTheAllowedFile: a download carrying both an
// allowed and a disallowed format must not be blocked whole. The per-file skip
// takes the epub and leaves the mobi, and nothing is blocklisted because the
// release did contain something the user wanted.
func TestImport_MixedDownloadImportsTheAllowedFile(t *testing.T) {
	f := newFormatFixture(t)
	f.writeFile(t, "book.epub", epubBytes())
	f.writeFile(t, "book.mobi", palmMobiBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, "")

	got := f.reloadDownload(t)
	if got.Status == models.StateImportBlocked {
		t.Fatalf("a mixed download was blocked whole: %s", got.ErrorMessage)
	}
	blocked, err := f.blocklist.IsBlocked(ctx, "guid-bloodline")
	if err != nil {
		t.Fatalf("check blocklist: %v", err)
	}
	if blocked {
		t.Error("a release containing an allowed format was blocklisted")
	}
}

// TestImport_ExplicitFormatHintOverrides: a manual import is a human declaring
// the format, and their call wins. Same principle as the video guard.
func TestImport_ExplicitFormatHintOverrides(t *testing.T) {
	f := newFormatFixture(t)
	f.writeFile(t, "book.mobi", palmMobiBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, models.MediaTypeEbook)

	got := f.reloadDownload(t)
	if got.Status == models.StateImportBlocked {
		t.Fatalf("an explicit manual import was blocked: %s", got.ErrorMessage)
	}
	blocked, err := f.blocklist.IsBlocked(ctx, "guid-bloodline")
	if err != nil {
		t.Fatalf("check blocklist: %v", err)
	}
	if blocked {
		t.Error("a manually forced import was blocklisted")
	}
}

// TestImport_NoEnforcementWiringIsInert: a scanner without the repos must
// behave exactly as before, which is what keeps every existing caller and test
// unaffected.
func TestImport_NoEnforcementWiringIsInert(t *testing.T) {
	f := newFormatFixture(t)
	f.scanner.qualityProfiles = nil
	f.scanner.blocklist = nil
	f.writeFile(t, "book.mobi", palmMobiBytes())
	ctx := context.Background()

	f.scanner.ImportFromPath(ctx, f.dl, f.dir, "")

	got := f.reloadDownload(t)
	if got.Status == models.StateImportBlocked {
		t.Errorf("format enforcement ran without being wired up: %s", got.ErrorMessage)
	}
}
