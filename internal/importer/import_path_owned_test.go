package importer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// pathOwnedFixture is the #2937 setup: two volumes of one series, the right
// one (Vol 3) Wanted and about to import, the wrong one (Vol 17) already
// holding a book_files row for the exact path Vol 3's import will land on.
type pathOwnedFixture struct {
	database  *sql.DB
	scanner   *Scanner
	books     *db.BookRepo
	downloads *db.DownloadRepo
	history   *db.HistoryRepo
	notif     *spyNotifier
	author    *models.Author
	right     *models.Book
	wrong     *models.Book
	dl        *models.Download
	dir       string
	library   string
	settings  *db.SettingsRepo
}

func newPathOwnedFixture(t *testing.T, mediaType string) *pathOwnedFixture {
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

	author := &models.Author{
		ForeignID: "OL-OWNED-A", Name: "TJF Saga", SortName: "Saga, TJF",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mk := func(foreign, title string) *models.Book {
		b := &models.Book{
			ForeignID: foreign, AuthorID: author.ID, Title: title,
			SortTitle: strings.ToLower(title), Status: models.BookStatusWanted, Monitored: true,
			AnyEditionOK: true, MediaType: mediaType, MetadataProvider: "openlibrary",
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	right := mk("OL-OWNED-3", "Defiance of the Fall 3")
	wrong := mk("OL-OWNED-17", "Defiance of the Fall 17")

	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	library := t.TempDir()
	notif := &spyNotifier{}
	s := NewScanner(downloads, clients, books, authors, history, library, "", "", "", "")
	s.WithSettings(settings)
	s.WithNotifier(notif)

	dl := &models.Download{
		BookID: &right.ID, GUID: "guid-dotf-3", Title: "Defiance.of.the.Fall.3",
		Status: models.StateCompleted, Protocol: "torrent",
	}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return &pathOwnedFixture{
		database: database, scanner: s, books: books, downloads: downloads, history: history,
		notif: notif, author: author, right: right, wrong: wrong, dl: dl,
		dir: dir, library: library, settings: settings,
	}
}

// ebookDest writes the downloaded EPUB and returns the library path the
// import will place it at.
func (f *pathOwnedFixture) ebookDest(t *testing.T) string {
	t.Helper()
	src := filepath.Join(f.dir, "Defiance of the Fall 3.epub")
	if err := os.WriteFile(src, []byte("volume three"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest, err := f.scanner.destRenamer(context.Background()).DestPath(f.library, f.author, f.right, "", "", src)
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func (f *pathOwnedFixture) importDownload(t *testing.T) {
	t.Helper()
	f.scanner.tryImportInternal(context.Background(), f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)
}

func (f *pathOwnedFixture) reload(t *testing.T) (*models.Download, *models.Book) {
	t.Helper()
	ctx := context.Background()
	dl, err := f.downloads.GetByID(ctx, f.dl.ID)
	if err != nil || dl == nil {
		t.Fatalf("reload download: %v", err)
	}
	book, err := f.books.GetByID(ctx, f.right.ID)
	if err != nil || book == nil {
		t.Fatalf("reload book: %v", err)
	}
	return dl, book
}

func (f *pathOwnedFixture) files(t *testing.T, bookID int64) []models.BookFile {
	t.Helper()
	files, err := f.books.ListFiles(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (f *pathOwnedFixture) historyOf(t *testing.T, eventType string) []models.HistoryEvent {
	t.Helper()
	events, err := f.history.ListByType(context.Background(), eventType)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// assertBlockedNamingOwner is what every owned path case must end in: the
// download blocked with a reason naming the book that holds the path, an
// importFailed row and notification, no bookImported row, and the right book
// still Wanted with nothing recorded against it.
func (f *pathOwnedFixture) assertBlockedNamingOwner(t *testing.T) {
	t.Helper()
	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q (%s), want %q: the import recorded nothing yet reported success",
			dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	assertOwnerReason(t, dl.ErrorMessage, f.wrong)
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want %q", book.Status, models.BookStatusWanted)
	}
	if files := f.files(t, f.right.ID); len(files) != 0 {
		t.Errorf("right book has files %+v, want none", files)
	}
	if got := f.historyOf(t, models.HistoryEventBookImported); len(got) != 0 {
		t.Errorf("got %d bookImported history rows for an import that recorded nothing: %+v", len(got), got)
	}
	if got := f.historyOf(t, models.HistoryEventImportFailed); len(got) != 1 {
		t.Errorf("got %d importFailed history rows, want 1", len(got))
	}
	if f.notif.lookup(notifierEventDownloadFailed) == nil {
		t.Error("no downloadFailed notification for the blocked import")
	}
	if f.notif.lookup(notifierEventBookImported) != nil {
		t.Error("bookImported notification sent for an import that recorded nothing")
	}
}

// TestImport_EbookPathOwnedByOtherBook_FailsNamingOwner is the #2937 report.
// Vol 17 already tracks the file Vol 3's download lands on. Before the fix
// the INSERT OR IGNORE recorded nothing, the import reported success with a
// history row naming Vol 3, and Vol 3 stayed Wanted with no file. The commit
// rename also replaced the file Vol 17 tracks.
func TestImport_EbookPathOwnedByOtherBook_FailsNamingOwner(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	dest := f.ebookDest(t)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("tracked by vol 17"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, dest); err != nil {
		t.Fatal(err)
	}

	f.importDownload(t)

	f.assertBlockedNamingOwner(t)
	owner := f.files(t, f.wrong.ID)
	if len(owner) != 1 || owner[0].Path != dest {
		t.Errorf("owning book's files = %+v, want its row for %s untouched", owner, dest)
	}
	if got, _ := os.ReadFile(dest); string(got) != "tracked by vol 17" { //nolint:gosec // test path under t.TempDir
		t.Errorf("file the other book tracks was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "Defiance of the Fall 3.epub")); err != nil {
		t.Errorf("download source gone after a blocked import: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bindery-stage-") {
			t.Errorf("staged file %s left behind", e.Name())
		}
	}
}

// TestImport_AudiobookPathOwnedByOtherBook_FailsNamingOwner covers the
// SetFormatFilePath branch the issue names: the audiobook folder Vol 3's
// import computes is already registered to Vol 17.
func TestImport_AudiobookPathOwnedByOtherBook_FailsNamingOwner(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeAudiobook)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.dir, "Defiance of the Fall 3.m4b"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	dest, err := f.scanner.destRenamer(ctx).AudiobookDestDir(f.scanner.effectiveAudiobookDir(ctx, f.author), f.author, f.right, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.books.SetFormatFilePath(ctx, f.wrong.ID, models.MediaTypeAudiobook, dest); err != nil {
		t.Fatal(err)
	}

	f.importDownload(t)

	f.assertBlockedNamingOwner(t)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("copied audiobook folder left at %s after a blocked import (stat err %v)", dest, err)
	}
}

// assertOwnerReason checks a failure reason names the owning book and points
// at Fix match, never at a retry: a retry cannot record a path another book
// holds, and in move mode it would find the download already gone.
func assertOwnerReason(t *testing.T, reason string, owner *models.Book) {
	t.Helper()
	for _, want := range []string{owner.Title, fmt.Sprintf("id %d", owner.ID), "Fix match"} {
		if !strings.Contains(reason, want) {
			t.Errorf("failure message %q does not contain %q", reason, want)
		}
	}
	if strings.Contains(strings.ToLower(reason), "retry") {
		t.Errorf("failure message %q advises a retry that cannot succeed", reason)
	}
}

// TestImport_AudiobookMoveModePathOwned_PointsAtOwner: in move mode the
// folder is already placed and the source consumed, so the files stay where
// they landed; the reason must still name the owner rather than suggest a
// retry.
func TestImport_AudiobookMoveModePathOwned_PointsAtOwner(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeAudiobook)
	ctx := context.Background()
	if err := f.settings.Set(ctx, "import.mode", "move"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "Defiance of the Fall 3.m4b"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	dest, err := f.scanner.destRenamer(ctx).AudiobookDestDir(f.scanner.effectiveAudiobookDir(ctx, f.author), f.author, f.right, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.books.SetFormatFilePath(ctx, f.wrong.ID, models.MediaTypeAudiobook, dest); err != nil {
		t.Fatal(err)
	}

	f.importDownload(t)

	f.assertBlockedNamingOwner(t)
	dl, _ := f.reload(t)
	if !strings.Contains(dl.ErrorMessage, dest) {
		t.Errorf("failure message %q does not say where the moved files are (%s)", dl.ErrorMessage, dest)
	}
	if entries, err := os.ReadDir(dest); err != nil || len(entries) == 0 {
		t.Errorf("moved audiobook not preserved at %s: %v entries, err %v", dest, len(entries), err)
	}
}

// TestImport_MultiFileOneOwned_BlockedNotRetried: an epub and a mobi where
// only the epub's destination is held by another book. The mobi imports; the
// download is blocked naming the owner instead of ending importFailed, which
// would spend the retry budget on a conflict a retry cannot clear.
func TestImport_MultiFileOneOwned_BlockedNotRetried(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	epubDest := f.ebookDest(t)
	mobi := filepath.Join(f.dir, "Defiance of the Fall 3.mobi")
	if err := os.WriteFile(mobi, []byte("volume three mobi"), 0o600); err != nil {
		t.Fatal(err)
	}
	mobiDest, err := f.scanner.destRenamer(ctx).DestPath(f.library, f.author, f.right, "", "", mobi)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, epubDest); err != nil {
		t.Fatal(err)
	}

	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	assertOwnerReason(t, dl.ErrorMessage, f.wrong)
	files := f.files(t, f.right.ID)
	if len(files) != 1 || files[0].Path != mobiDest {
		t.Errorf("Vol 3 files = %+v, want only the mobi at %s", files, mobiDest)
	}
	if owner := f.files(t, f.wrong.ID); len(owner) != 1 || owner[0].Path != epubDest {
		t.Errorf("Vol 17 files = %+v, want its epub row untouched", owner)
	}
}

// TestImport_MultiFileOtherFailure_StaysRetryable: a partial import whose
// failure is not an ownership conflict keeps the old retryable status.
func TestImport_MultiFileOtherFailure_StaysRetryable(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	epubDest := f.ebookDest(t)
	mobi := filepath.Join(f.dir, "Defiance of the Fall 3.mobi")
	if err := os.WriteFile(mobi, []byte("volume three mobi"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory where the epub must land makes its commit rename fail,
	// a plain I/O failure rather than an ownership conflict.
	if err := os.MkdirAll(filepath.Join(epubDest, "occupied"), 0o750); err != nil {
		t.Fatal(err)
	}

	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportFailed {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportFailed)
	}
}

// TestImport_PathOwnedByOrphanRow_Repoints: the row holding the path belongs
// to a book that no longer exists (foreign_keys was off when it was deleted,
// #1727). That row is clearly stale, so the import takes it over.
func TestImport_PathOwnedByOrphanRow_Repoints(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	dest := f.ebookDest(t)
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, dest); err != nil {
		t.Fatal(err)
	}
	orphanBook(t, f.database, f.wrong.ID)

	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want %q", book.Status, models.BookStatusImported)
	}
	files := f.files(t, f.right.ID)
	if len(files) != 1 || files[0].Path != dest {
		t.Errorf("right book files = %+v, want the row for %s", files, dest)
	}
}

// TestImport_FixMatchMovesOwnedSourcePath: Fix match says the file belongs
// to Vol 3, so when the path it lands on is the reassigned file itself and
// Vol 17 still holds it (the handler's detach missed), the row moves to Vol 3
// and the move is recorded naming Vol 17.
func TestImport_FixMatchMovesOwnedSourcePath(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	dest := f.ebookDest(t)
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, dest); err != nil {
		t.Fatal(err)
	}

	fixCtx := WithFixMatch(ctx, FixMatch{SourcePaths: []string{dest}})
	f.scanner.tryImportInternal(fixCtx, f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)

	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want imported", book.Status)
	}
	if files := f.files(t, f.right.ID); len(files) != 1 || files[0].Path != dest {
		t.Errorf("Vol 3 files = %+v, want the moved row", files)
	}
	if files := f.files(t, f.wrong.ID); len(files) != 0 {
		t.Errorf("Vol 17 still has %+v", files)
	}
	wrong, _ := f.books.GetByID(ctx, f.wrong.ID)
	if wrong.Status != models.BookStatusWanted {
		t.Errorf("Vol 17 status = %q, want wanted after losing its only file", wrong.Status)
	}
	moved := f.historyOf(t, models.HistoryEventBookFileMoved)
	if len(moved) != 1 {
		t.Fatalf("got %d bookFileMoved rows, want 1", len(moved))
	}
	if moved[0].BookID == nil || *moved[0].BookID != f.right.ID || !strings.Contains(moved[0].Data, f.wrong.Title) {
		t.Errorf("move row = %+v, want it on Vol 3 naming %q", moved[0], f.wrong.Title)
	}
}

// TestImport_FixMatchDoesNotTakeUnrelatedPath: a Fix match authorises moving
// the file the user reassigned, nothing else. A destination another book
// holds that is not that file still fails.
func TestImport_FixMatchDoesNotTakeUnrelatedPath(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	dest := f.ebookDest(t)
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, dest); err != nil {
		t.Fatal(err)
	}

	fixCtx := WithFixMatch(ctx, FixMatch{SourcePaths: []string{filepath.Join(f.dir, "elsewhere.epub")}})
	f.scanner.tryImportInternal(fixCtx, f.dl, f.dir, "qbittorrent", "abc123", "", nil, nil)

	f.assertBlockedNamingOwner(t)
	if got := f.historyOf(t, models.HistoryEventBookFileMoved); len(got) != 0 {
		t.Errorf("got %d bookFileMoved rows for a refused import", len(got))
	}
}

// orphanBook deletes a book with foreign_keys off, leaving its book_files
// rows behind the way a lost pragma did (#1727).
func orphanBook(t *testing.T, database *sql.DB, bookID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM books WHERE id = ?", bookID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
}
