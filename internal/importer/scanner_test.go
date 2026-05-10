package importer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/calibre"
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
	}

	for _, tt := range tests {
		got := titleMatch(tt.bookTitle, tt.parsedTitle)
		if got != tt.want {
			t.Errorf("titleMatch(%q, %q) = %v, want %v", tt.bookTitle, tt.parsedTitle, got, tt.want)
		}
	}
}

// fakeCalibreAdder is a stub calibreAdder recording every Add invocation.
// Tests check both the call path and the book-id persistence so a broken
// wiring change surfaces here rather than in a live import.
type fakeCalibreAdder struct {
	calls  []string
	nextID int64
	err    error
}

func (f *fakeCalibreAdder) Add(_ context.Context, path string) (int64, error) {
	f.calls = append(f.calls, path)
	return f.nextID, f.err
}

func modeFn(m calibre.Mode) func() calibre.Mode { return func() calibre.Mode { return m } }

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

// TestPushToCalibre_ModeOff: regression guard for "integration off" —
// mode=off must mean zero client calls and no calibre_id mutation.
func TestPushToCalibre_ModeOff(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{nextID: 99}
	s.WithCalibre(modeFn(calibre.ModeOff), fc)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	if len(fc.calls) != 0 {
		t.Errorf("Add must not be called when mode=off, got %v", fc.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil when mode=off, got %v", got.CalibreID)
	}
}

func TestPushToCalibre_ModeCalibredbHappyPath(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{nextID: 1234}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	if len(fc.calls) != 1 || fc.calls[0] != "/library/book.epub" {
		t.Errorf("Add calls = %v", fc.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 1234 {
		t.Errorf("calibre_id = %v, want 1234", got.CalibreID)
	}
}

// TestPushToCalibre_CalibredbFailDoesNotPoison: a failed calibredb call
// must leave calibre_id at nil (best-effort mirror semantics).
func TestPushToCalibre_CalibredbFailDoesNotPoison(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{err: errors.New("exec: calibredb: not found")}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must remain nil on add failure, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ErrDisabledSilent — the adder may return ErrDisabled
// when the client's own config is off; we treat it the same as mode=off.
func TestPushToCalibre_ErrDisabledSilent(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{err: calibre.ErrDisabled}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil on ErrDisabled, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ModePluginHappyPath: mode=plugin routes through the
// plugin HTTP client. A fake server returning {"id":5678} must produce a
// persisted calibre_id of 5678.
func TestPushToCalibre_ModePluginHappyPath(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":5678,"duplicate":false}`))
	}))
	defer srv.Close()

	client := calibre.NewPluginClient(srv.URL, "test-key")
	s.WithCalibre(modeFn(calibre.ModePlugin), client)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 5678 {
		t.Errorf("calibre_id = %v, want 5678", got.CalibreID)
	}
}

// TestPushToCalibre_NilResolver covers the default path — a scanner built
// without WithCalibre() (i.e. calibre not configured at all) must not
// panic on a nil interface dereference.
func TestPushToCalibre_NilResolver(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	// No WithCalibre call.
	s.pushToCalibre(ctx, book, author, "/library/book.epub") // must not panic
}

// TestImportInternal_ThreeFileBundle_TracksAllInBookFiles verifies the #343
// fix: importing a multi-format download (epub + mobi + pdf) stores a
// separate book_files row for each file rather than overwriting a single path.
func TestImportInternal_ThreeFileBundle_TracksAllInBookFiles(t *testing.T) {
	libDir := t.TempDir()
	dlDir := t.TempDir()

	// Create three book files that simulate a multi-format NZB download.
	for _, name := range []string{"book.epub", "book.mobi", "book.pdf"} {
		if err := os.WriteFile(filepath.Join(dlDir, name), []byte("x"), 0o644); err != nil {
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
	s.tryImportInternal(ctx, dl, dlDir, "", "", nil)

	files, err := bookRepo.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("want 3 book_files rows for epub+mobi+pdf bundle, got %d", len(files))
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

	if err := os.WriteFile(filepath.Join(dlDir, "book.epub"), []byte("x"), 0o644); err != nil {
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
	s.tryImportInternal(ctx, dl, dlDir, "", "", nil)

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
