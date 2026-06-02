package importer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	metas  []calibre.Metadata
	nextID int64
	err    error
}

func (f *fakeCalibreAdder) Add(_ context.Context, path string, meta calibre.Metadata) (int64, error) {
	f.calls = append(f.calls, path)
	f.metas = append(f.metas, meta)
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

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub")

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

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub")

	if len(fc.calls) != 1 || fc.calls[0] != "/library/book.epub" {
		t.Errorf("Add calls = %v", fc.calls)
	}
	if len(fc.metas) != 1 || fc.metas[0].Title != "Title T" || len(fc.metas[0].Authors) != 1 || fc.metas[0].Authors[0] != "Author A" {
		t.Errorf("metadata = %+v, want book title and author", fc.metas)
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

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub")

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

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub")

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

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub")

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
	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/book.epub") // must not panic
}

func TestCalibreMetadata_PrefersEditionFieldsAndMapsSeries(t *testing.T) {
	ctx := context.Background()
	published := time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC)
	release := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	asin := "B000FC1BN8"
	book := &models.Book{
		ID:               42,
		ForeignID:        "OL123W",
		Title:            "Dune",
		Description:      "Desert planet.",
		ReleaseDate:      &release,
		Genres:           []string{"Science Fiction", "Classics"},
		AverageRating:    4.6,
		Language:         "eng",
		ASIN:             "BOOKASIN",
		MetadataProvider: "openlibrary",
	}
	author := &models.Author{Name: "Frank Herbert", SortName: "Herbert, Frank"}
	edition := &models.Edition{
		ForeignID:   "OL999M",
		ISBN13:      strPtr("9780441172719"),
		ASIN:        &asin,
		Publisher:   "Ace",
		PublishDate: &published,
		Language:    "ger",
		ImageURL:    "",
	}

	s := NewScanner(nil, nil, nil, nil, nil, t.TempDir(), "", "", "", "")
	meta := s.calibreMetadata(ctx, book, author, edition, "Dune Chronicles", "1", calibre.ModeCalibredb)

	if meta.Title != "Dune" || len(meta.Authors) != 1 || meta.Authors[0] != "Frank Herbert" {
		t.Fatalf("basic metadata = %+v", meta)
	}
	if meta.AuthorSort != "Herbert, Frank" || meta.Description != "Desert planet." {
		t.Fatalf("author/description metadata = %+v", meta)
	}
	if meta.Language != "de" {
		t.Fatalf("Language = %q, want de from edition language", meta.Language)
	}
	if meta.PublishedDate != "2020-03-04" {
		t.Fatalf("PublishedDate = %q, want edition date", meta.PublishedDate)
	}
	if meta.Publisher != "Ace" || meta.Series != "Dune Chronicles" || meta.SeriesIndex != "1" {
		t.Fatalf("publisher/series metadata = %+v", meta)
	}
	if meta.Identifiers["isbn"] != "9780441172719" {
		t.Fatalf("isbn identifier = %q", meta.Identifiers["isbn"])
	}
	if meta.Identifiers["asin"] != "B000FC1BN8" {
		t.Fatalf("asin identifier = %q, want edition ASIN", meta.Identifiers["asin"])
	}
	if meta.Identifiers["bindery"] != "42" || meta.Identifiers["openlibrary"] != "OL123W" {
		t.Fatalf("provider identifiers = %+v", meta.Identifiers)
	}
	if meta.Identifiers["openlibrary_edition"] != "OL999M" {
		t.Fatalf("openlibrary edition identifier = %q, want OL999M", meta.Identifiers["openlibrary_edition"])
	}
}

func TestCalibreMetadata_NormalizesPresentProviderIdentifiers(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		provider  string
		foreignID string
		wantType  string
		wantValue string
	}{
		{"openlibrary", "openlibrary", "/works/OL123W", "openlibrary", "OL123W"},
		{"hardcover", "hardcover", "hc:dune", "hardcover", "dune"},
		{"googlebooks", "googlebooks", "gb:zyTCAlFPjgYC", "google", "zyTCAlFPjgYC"},
		{"dnb", "dnb", "dnb:123456789", "dnb", "123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book := &models.Book{
				ID:               42,
				ForeignID:        tt.foreignID,
				Title:            "Dune",
				MetadataProvider: tt.provider,
			}
			s := NewScanner(nil, nil, nil, nil, nil, t.TempDir(), "", "", "", "")
			meta := s.calibreMetadata(ctx, book, nil, nil, "", "", calibre.ModePlugin)
			if meta.Identifiers[tt.wantType] != tt.wantValue {
				t.Fatalf("identifier %q = %q, want %q in %+v", tt.wantType, meta.Identifiers[tt.wantType], tt.wantValue, meta.Identifiers)
			}
			if meta.Identifiers["bindery"] != "42" {
				t.Fatalf("bindery identifier = %q, want 42", meta.Identifiers["bindery"])
			}
		})
	}
}

func TestCalibreMetadata_CoverPathOnlyForCalibredb(t *testing.T) {
	ctx := context.Background()
	cacheDir := t.TempDir()
	imageURL := "https://93.184.216.34/cover.jpg"
	sum := sha256.Sum256([]byte(imageURL))
	coverPath := filepath.Join(cacheDir, fmt.Sprintf("%x.jpg", sum))
	if err := os.WriteFile(coverPath, []byte("cached cover"), 0o640); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ID:       42,
		Title:    "Dune",
		ImageURL: imageURL,
		Genres:   []string{},
	}
	s := NewScanner(nil, nil, nil, nil, nil, t.TempDir(), "", "", "", "").WithCalibreCoverCache(cacheDir)

	calibredbMeta := s.calibreMetadata(ctx, book, nil, nil, "", "", calibre.ModeCalibredb)
	if calibredbMeta.CoverPath != coverPath {
		t.Fatalf("calibredb CoverPath = %q, want %q", calibredbMeta.CoverPath, coverPath)
	}

	pluginMeta := s.calibreMetadata(ctx, book, nil, nil, "", "", calibre.ModePlugin)
	if pluginMeta.CoverPath != "" {
		t.Fatalf("plugin CoverPath = %q, want empty", pluginMeta.CoverPath)
	}
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
	s.tryImportInternal(ctx, dl, dlDir, "", "", nil, nil)

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
	s.tryImportInternal(ctx, dl, dlDir, "", "", nil, nil)

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
	s.tryImportInternal(ctx, dl, dlDir, "", "", nil, nil)

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
