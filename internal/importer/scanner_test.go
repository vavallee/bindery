package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Leading-article forms are stripped
		{"A Darker Shade of Magic", "darker shade of magic"},
		{"An Ember in the Ashes", "ember in the ashes"},
		{"The Fragile Threads of Power", "fragile threads of power"},
		// Comma-suffix forms are inverted then stripped
		{"Darker Shade of Magic, A", "darker shade of magic"},
		{"Ember in the Ashes, An", "ember in the ashes"},
		{"Fragile Threads of Power, The", "fragile threads of power"},
		// No article — unchanged (lowercased)
		{"Project Hail Mary", "project hail mary"},
		// Already normalised
		{"darker shade of magic", "darker shade of magic"},
	}
	for _, tt := range tests {
		if got := normalizeTitle(tt.in); got != tt.want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTitleMatch(t *testing.T) {
	tests := []struct {
		bookTitle   string
		parsedTitle string
		want        bool
	}{
		// Standard matches
		{"The Name of the Wind", "The Name of the Wind", true},
		{"Project Hail Mary", "Project Hail Mary", true},
		{"The Way of Kings", "Brandon Sanderson The Way of Kings", true},

		// Partial overlap — at least 2 significant (non-stopword) words required
		{"Dune Messiah", "Frank Herbert Dune Messiah", true},
		{"The Road", "Cormac McCarthy The Road 2006", true},

		// Single-token book title: minLen=1 → required=1; one matching token is enough
		{"Dune", "Frank Herbert Dune", true},
		{"Dune", "Dune 2021", true},
		{"The Sparrow", "The Sparrow Russell", true},

		// Numeric titles preserved (digits are kept as tokens)
		{"1984", "1984", true},
		{"1984", "George Orwell 1984", true},

		// Article inversion: comma-suffix form matches leading-article DB title
		{"The Lord of the Rings", "Lord of the Rings, The", true},
		{"A Darker Shade of Magic", "Darker Shade of Magic, A", true},
		{"An Ember in the Ashes", "Ember in the Ashes, An", true},

		// Dots in parsed title are split on non-alnum — "Project.Hail.Mary" yields 3 tokens
		{"Project Hail Mary", "Project.Hail.Mary", true},

		// Empty / degenerate cases
		{"", "The Name of the Wind", false},
		{"The Name of the Wind", "", false},

		// Noise titles with no overlap
		{"Project Hail Mary", "The Lord of the Rings", false},
		{"Dune", "Foundation Asimov", false},

		// Volumes of one series are different works however many words they
		// share (#2810): a bare trailing number and an explicit marker alike.
		{"Defiance of the Fall 17", "Defiance of the Fall 01", false},
		{"Defiance of the Fall 7", "Defiance of the Fall 17", false},
		{"Overlord, Vol. 1", "Overlord, Vol. 9", false},
		// The same volume still matches across zero padding, and an unnumbered
		// first volume still matches its numbered folder.
		{"Defiance of the Fall 1", "Defiance of the Fall 01", true},
		{"Defiance of the Fall", "Defiance of the Fall 01", true},
		// A number that is part of the title is not a volume.
		{"Fahrenheit 451", "Ray Bradbury Fahrenheit 451", true},
		{"Catch-22", "Catch 22", true},
		{"11/22/63", "11-22-63", true},
		// A multi-file audiobook's "Part N" counts files, not books, so it
		// cannot veto a series position spelled another way. Two Part
		// markers are still two halves of a split edition.
		{"Rhythm of War (The Stormlight Archive, Book 4)", "Rhythm of War Part 1", true},
		{"Rhythm of War (The Stormlight Archive #4)", "Rhythm of War Pt. 2 of 3", true},
		{"The Way of Kings, Part 1", "The Way of Kings, Part 2", false},
	}

	for _, tt := range tests {
		got := titleMatch(tt.bookTitle, tt.parsedTitle)
		if got != tt.want {
			t.Errorf("titleMatch(%q, %q) = %v, want %v", tt.bookTitle, tt.parsedTitle, got, tt.want)
		}
	}
}

func importScannerFixture(t *testing.T) (*Scanner, *db.BookRepo, *models.Book, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)

	a := &models.Author{ForeignID: "OLA1", Name: "Author A", SortName: "A, Author", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OLB1", AuthorID: a.ID, Title: "Title T", SortTitle: "T, Title",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(
		db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		bookRepo, authorRepo, db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	)
	return s, bookRepo, b, a, ctx
}

func TestResolveCalibreEdition_PrefersDownloadThenSelected(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	editionRepo := db.NewEditionRepo(database)
	author := &models.Author{ForeignID: "A", Name: "Author", SortName: "Author", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "B", AuthorID: author.ID, Title: "Book", SortTitle: "Book", Monitored: true, Status: models.BookStatusWanted, Genres: []string{}}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	selected := &models.Edition{ForeignID: "E1", BookID: book.ID, Title: "Selected", ISBN13: strPtr("111"), IsEbook: true}
	downloaded := &models.Edition{ForeignID: "E2", BookID: book.ID, Title: "Downloaded", ISBN13: strPtr("222"), IsEbook: true}
	if err := editionRepo.Upsert(ctx, selected); err != nil {
		t.Fatal(err)
	}
	if err := editionRepo.Upsert(ctx, downloaded); err != nil {
		t.Fatal(err)
	}
	book.SelectedEditionID = &selected.ID

	s := NewScanner(nil, nil, bookRepo, authorRepo, nil, t.TempDir(), "", "", "", "").WithEditions(editionRepo)
	dl := &models.Download{EditionID: &downloaded.ID}
	got := s.resolveCalibreEdition(ctx, dl, book)
	if got == nil || got.ID != downloaded.ID {
		t.Fatalf("download edition = %+v, want %d", got, downloaded.ID)
	}
	got = s.resolveCalibreEdition(ctx, &models.Download{}, book)
	if got == nil || got.ID != selected.ID {
		t.Fatalf("selected edition = %+v, want %d", got, selected.ID)
	}
}

func strPtr(s string) *string { return &s }

// TestImportInternal_ThreeFileBundle_TracksAllInBookFiles verifies the #343
// fix: importing a multi-format download (epub + mobi + pdf) stores a
// separate book_files row for each file rather than overwriting a single path.
func TestImportInternal_ThreeFileBundle_TracksAllInBookFiles(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	// Create three book files that simulate a multi-format NZB download.
	for _, name := range []string{"book.epub", "book.mobi", "book.pdf"} {
		if err := os.WriteFile(filepath.Join(dlDir, name), bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	author := &models.Author{ForeignID: "OLA-3F", Name: "Author", SortName: "Author", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-3F", AuthorID: author.ID, Title: "Three Formats",
		SortTitle: "Three Formats", Status: models.BookStatusWanted,
		Monitored: true, AnyEditionOK: true, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID: "3f-guid", Title: "Three Formats", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, db.NewHistoryRepo(database), libDir, "", "", "", "")
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("want 3 book_files rows for epub+mobi+pdf bundle, got %d", len(files))
	}
}

// TestImportInternal_OPFSidecarSeesBackfilledLanguage regression-tests the
// ordering fix: the OPF sidecar is written after applyEmbeddedLanguage's
// #1160 backfill, not before it, so a first-time import of a book with no
// catalogue language produces a sidecar that already carries the language
// read from the EPUB rather than a blank dc:language moments before the DB
// catches up.
func TestImportInternal_OPFSidecarSeesBackfilledLanguage(t *testing.T) {
	dlDir := t.TempDir()

	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Cien Años de Soledad</dc:title>
    <dc:creator opf:role="aut">Gabriel García Márquez</dc:creator>
    <dc:language>es</dc:language>
  </metadata>
</package>`
	epubSrc := writeTestEpub(t, "OEBPS/content.opf", opf)
	epubBytes, err := os.ReadFile(epubSrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "book.epub"), epubBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	libDir := t.TempDir()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	if err := settingsRepo.Set(ctx, "import.write_opf_sidecar", "true"); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "OLA-LANG", Name: "Author", SortName: "Author", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-LANG", AuthorID: author.ID, Title: "Cien Años de Soledad",
		SortTitle: "Cien Años de Soledad", Status: models.BookStatusWanted,
		Monitored: true, AnyEditionOK: true, MetadataProvider: "openlibrary",
		Language: "", // no catalogue language — triggers the #1160 backfill path
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID: "lang-guid", Title: "Cien Años de Soledad", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, db.NewHistoryRepo(database), libDir, "", "", "", "").WithSettings(settingsRepo)
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	updated, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Language != "spa" {
		t.Fatalf("book language after import = %q, want backfilled %q", updated.Language, "spa")
	}

	bookFiles, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(bookFiles) != 1 {
		t.Fatalf("want 1 book_files row, got %d", len(bookFiles))
	}
	sidecarPath := filepath.Join(filepath.Dir(bookFiles[0].Path), "metadata.opf")
	sidecar, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading metadata.opf: %v", err)
	}
	if !strings.Contains(string(sidecar), "<dc:language>es</dc:language>") {
		t.Errorf("metadata.opf missing backfilled language (want normalized <dc:language>es</dc:language>), got:\n%s", sidecar)
	}
}

// TestTryImportInternal_HistoryEventIncludesFormat is the regression test for
// Bug #13. When an ebook is imported for a media_type='both' book the
// bookImported history event must carry a "format" field so the user can see
// which format was actually imported — without it the queue shows "imported"
// with no indication that the audiobook half is still missing.
func TestTryImportInternal_HistoryEventIncludesFormat(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dlDir, "book.epub"), bookSized("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	author := &models.Author{
		ForeignID: "OLA-B13", Name: "Author B13", SortName: "B13, Author",
		Monitored: true, MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-B13", AuthorID: author.ID,
		Title: "Both Format Book", SortTitle: "Both Format Book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeBoth, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID: "b13-guid", Title: "Both Format Book", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, historyRepo, libDir, "", "", "", "")
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	events, err := historyRepo.ListByType(ctx, models.HistoryEventBookImported)
	if err != nil {
		t.Fatalf("list history events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("Bug #13: no bookImported history event was created")
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatalf("unmarshal history event data: %v", err)
	}
	if got := data["format"]; got != models.MediaTypeEbook {
		t.Errorf("Bug #13: history event missing format field: got %q, want %q — user cannot tell ebook vs audiobook was imported", got, models.MediaTypeEbook)
	}
}

// spyNotifier records every Send so emit-site tests can assert the
// notification was published with the expected event type and payload.
type spyNotifier struct {
	mu    sync.Mutex
	calls []spyCall
}

type spyCall struct {
	eventType string
	payload   map[string]interface{}
}

func (n *spyNotifier) Send(_ context.Context, eventType string, payload map[string]interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, spyCall{eventType: eventType, payload: payload})
}

func (n *spyNotifier) lookup(eventType string) *spyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.calls {
		if n.calls[i].eventType == eventType {
			return &n.calls[i]
		}
	}
	return nil
}

// TestImportSuccess_FiresBookImported is the regression test for issue #849:
// before this fix, only manual grabs from the queue page fired notifications.
// A successful import wrote a HistoryEventBookImported row but never published
// to the user-configured webhooks. After the fix, every clean import must
// emit EventBookImported with the book title and format.
func TestImportSuccess_FiresBookImported(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dlDir, "book.epub"), bookSized("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	author := &models.Author{ForeignID: "OLA-849", Name: "Notif Author", SortName: "Author, Notif", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB-849", AuthorID: author.ID, Title: "Issue 849",
		SortTitle: "Issue 849", Status: models.BookStatusWanted,
		Monitored: true, AnyEditionOK: true, MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{
		GUID: "849-guid", Title: "Issue 849", BookID: &book.ID,
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	spy := &spyNotifier{}
	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, db.NewHistoryRepo(database), libDir, "", "", "", "").
		WithNotifier(spy)
	s.tryImportInternal(ctx, dl, dlDir, "", "", "", nil, nil)

	call := spy.lookup(notifierEventBookImported)
	if call == nil {
		t.Fatalf("expected EventBookImported to fire; got calls: %+v", spy.calls)
		return
	}
	if got, want := call.payload["title"], book.Title; got != want {
		t.Errorf("payload title = %q, want %q", got, want)
	}
	if got, want := call.payload["format"], models.MediaTypeEbook; got != want {
		t.Errorf("payload format = %q, want %q", got, want)
	}
}

// TestFailImport_FiresDownloadFailed asserts that failImport — the helper
// called for unwritable destinations, partial imports, unmatched downloads,
// etc. — publishes EventDownloadFailed (issue #849). The notifier has no
// dedicated EventImportFailed; downloadFailed is the channel for both.
func TestFailImport_FiresDownloadFailed(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)

	dl := &models.Download{GUID: "fail-guid", Title: "Broken Book", Status: models.StateImporting}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	spy := &spyNotifier{}
	s := &Scanner{downloads: dlRepo, history: db.NewHistoryRepo(database), notif: spy}

	s.failImport(ctx, dl, models.StateImportFailed, "destination unwritable")

	call := spy.lookup(notifierEventDownloadFailed)
	if call == nil {
		t.Fatalf("expected EventDownloadFailed to fire; got calls: %+v", spy.calls)
		return
	}
	if got, want := call.payload["title"], dl.Title; got != want {
		t.Errorf("payload title = %q, want %q", got, want)
	}
	if got, want := call.payload["message"], "destination unwritable"; got != want {
		t.Errorf("payload message = %q, want %q", got, want)
	}
}

// TestMarkDownloadFailed_FiresDownloadFailed asserts that the inline download-
// failure helper (used by Transmission/qBittorrent error paths) also fires
// EventDownloadFailed (issue #849).
func TestMarkDownloadFailed_FiresDownloadFailed(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	dl := &models.Download{GUID: "stall-guid", Title: "Stalled Book", Status: models.StateDownloading}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	spy := &spyNotifier{}
	s := &Scanner{downloads: dlRepo, history: db.NewHistoryRepo(database), notif: spy}
	s.markDownloadFailed(ctx, dl, "torrent errored")

	call := spy.lookup(notifierEventDownloadFailed)
	if call == nil {
		t.Fatalf("expected EventDownloadFailed; got calls: %+v", spy.calls)
		return
	}
	if got := call.payload["message"]; got != "torrent errored" {
		t.Errorf("payload message = %v, want %q", got, "torrent errored")
	}
}

// TestNotify_NilNotifierDoesNotPanic guards the optional-injection contract:
// a Scanner with no notifier set must silently skip emission, not crash.
func TestNotify_NilNotifierDoesNotPanic(t *testing.T) {
	s := &Scanner{}
	s.notify(context.Background(), notifierEventGrabbed, map[string]interface{}{"title": "x"})
}
