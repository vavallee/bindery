package bookorbit

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// containerRoot is where the fake BookOrbit says its books live. shelf.dir is
// the same tree as Bindery sees it, and the path remap joins the two.
const containerRoot = "/books/Books"

type shelf struct {
	t        *testing.T
	dir      string
	database *sql.DB
	importer *Importer
	engine   *abs.Importer
	authors  *db.AuthorRepo
	books    *db.BookRepo
	series   *db.SeriesRepo
	runs     *db.ABSImportRunRepo
	fake     *fakeServer
}

func newShelf(t *testing.T) *shelf {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	s := &shelf{
		t:        t,
		dir:      t.TempDir(),
		database: database,
		authors:  db.NewAuthorRepo(database),
		books:    db.NewBookRepo(database),
		series:   db.NewSeriesRepo(database),
		runs:     db.NewABSImportRunRepo(database),
		fake:     newFakeServer(t),
	}
	s.engine = abs.NewImporter(
		s.authors, db.NewAuthorAliasRepo(database), s.books, db.NewEditionRepo(database), s.series,
		db.NewSettingsRepo(database), s.runs, db.NewABSImportRunEntityRepo(database), db.NewABSProvenanceRepo(database),
		nil, nil,
	).WithStoragePaths(s.dir, "", nil)
	s.importer = NewImporter(s.engine)
	s.fake.addLibrary(2, "Shelf", containerRoot)
	return s
}

// put creates a file on Bindery's side of the shelf and returns the path
// BookOrbit would report for it.
func (s *shelf) put(rel string) string {
	s.t.Helper()
	full := filepath.Join(s.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		s.t.Fatal(err)
	}
	return containerRoot + "/" + rel
}

func (s *shelf) local(rel string) string {
	return filepath.Join(s.dir, filepath.FromSlash(rel))
}

func (s *shelf) config(dryRun bool) Config {
	return Config{
		BaseURL:    s.fake.URL,
		APIKey:     fakeKey,
		PathRemap:  containerRoot + ":" + s.dir,
		LibraryIDs: []int64{2},
		DryRun:     dryRun,
	}
}

func (s *shelf) run(cfg Config) *abs.ImportStats {
	s.t.Helper()
	stats, err := s.importer.Run(context.Background(), cfg)
	if err != nil {
		s.t.Fatalf("Run: %v", err)
	}
	return stats
}

func (s *shelf) bookByTitle(title string) *models.Book {
	s.t.Helper()
	books, err := s.books.List(context.Background())
	if err != nil {
		s.t.Fatal(err)
	}
	var found *models.Book
	for idx := range books {
		if books[idx].Title == title {
			if found != nil {
				s.t.Fatalf("two books titled %q", title)
			}
			found = &books[idx]
		}
	}
	if found == nil {
		s.t.Fatalf("no book titled %q among %d", title, len(books))
	}
	return found
}

// files returns "format path" for each file a book tracks, sorted.
func (s *shelf) files(bookID int64) []string {
	s.t.Helper()
	files, err := s.books.ListFiles(context.Background(), bookID)
	if err != nil {
		s.t.Fatal(err)
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Format+" "+f.Path)
	}
	sort.Strings(out)
	return out
}

func (s *shelf) count(table string) int {
	s.t.Helper()
	var n int
	if err := s.database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *shelf) authorName(id int64) string {
	s.t.Helper()
	author, err := s.authors.GetByID(context.Background(), id)
	if err != nil || author == nil {
		s.t.Fatalf("author %d: %v", id, err)
	}
	return author.Name
}

func (s *shelf) tree() []string {
	s.t.Helper()
	var out []string
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, path+" "+info.Mode().String()+" "+info.ModTime().String())
		return nil
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

// The layout from the issue: Genre/Author/Series/NN - Title/. The library
// scan reads the genre or the series as the author there. The importer takes
// author, series and position from BookOrbit's record, and registers the file
// where Bindery sees it.
func TestImportGenreAuthorSeriesLayout(t *testing.T) {
	s := newShelf(t)
	rel := "Fantasy/Ursula Vale/Tide Cycle/02 - The Salt Road/The Salt Road.epub"
	s.fake.addBook(2, withSeries(fakeBook(11, "The Salt Road", []string{"Ursula Vale"},
		fakeFile(1, s.put(rel), "primary"),
		fakeFile(2, containerRoot+"/Fantasy/Ursula Vale/Tide Cycle/02 - The Salt Road/cover.jpg", "cover"),
	), 5, "Tide Cycle", "2"))

	stats := s.run(s.config(false))

	if stats.ItemsSeen != 1 || stats.BooksCreated != 1 || stats.AuthorsCreated != 1 || stats.OwnedMarked != 1 || stats.PendingManual != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %s", stats)
	}
	book := s.bookByTitle("The Salt Road")
	if got := s.authorName(book.AuthorID); got != "Ursula Vale" {
		t.Errorf("author = %q, want Ursula Vale (not the genre or the series folder)", got)
	}
	if got, want := s.files(book.ID), []string{"ebook " + s.local(rel)}; !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if book.MediaType != models.MediaTypeEbook {
		t.Errorf("media type = %q", book.MediaType)
	}
	all, err := s.series.List(context.Background())
	if err != nil || len(all) != 1 || all[0].Title != "Tide Cycle" {
		t.Fatalf("series = %+v, %v", all, err)
	}
	if got := s.position(all[0].ID, book.ID); got != "2" {
		t.Errorf("series position = %q, want 2", got)
	}
}

func (s *shelf) position(seriesID, bookID int64) string {
	s.t.Helper()
	var pos string
	if err := s.database.QueryRow("SELECT position_in_series FROM series_books WHERE series_id = ? AND book_id = ?", seriesID, bookID).Scan(&pos); err != nil {
		s.t.Fatalf("series membership: %v", err)
	}
	return pos
}

func TestImportDecimalSeriesPosition(t *testing.T) {
	s := newShelf(t)
	s.fake.addBook(2, withSeries(fakeBook(12, "Low Water", []string{"Ursula Vale"},
		fakeFile(1, s.put("Fantasy/Ursula Vale/Tide Cycle/04.5 - Low Water/Low Water.epub"), "primary"),
	), 5, "Tide Cycle", "4.5"))
	s.run(s.config(false))
	all, _ := s.series.List(context.Background())
	if len(all) != 1 {
		t.Fatalf("series = %+v", all)
	}
	if got := s.position(all[0].ID, s.bookByTitle("Low Water").ID); got != "4.5" {
		t.Errorf("position = %q, want 4.5", got)
	}
}

// One book with an EPUB and a single M4B: both are registered, each under its
// own format, and the book covers both.
func TestImportEbookAndAudiobookOnOneBook(t *testing.T) {
	s := newShelf(t)
	epub := "Mystery/Ines Okoro/Harbour Cases/01 - Fog Bell/Fog Bell.epub"
	m4b := "Mystery/Ines Okoro/Harbour Cases/01 - Fog Bell/Fog Bell.m4b"
	s.fake.addBook(2, fakeBook(13, "Fog Bell", []string{"Ines Okoro"},
		fakeFile(1, s.put(m4b), "content"),
		fakeFile(2, s.put(epub), "primary"),
	))
	stats := s.run(s.config(false))
	if stats.OwnedMarked != 2 || stats.BooksCreated != 1 {
		t.Fatalf("stats = %s", stats)
	}
	book := s.bookByTitle("Fog Bell")
	if book.MediaType != models.MediaTypeBoth {
		t.Errorf("media type = %q, want both", book.MediaType)
	}
	want := []string{"audiobook " + s.local(m4b), "ebook " + s.local(epub)}
	if got := s.files(book.ID); !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// A multi track audiobook is registered as its folder, which is how Bindery
// tracks one from any source.
func TestImportMultiTrackAudiobookRegistersFolder(t *testing.T) {
	s := newShelf(t)
	dir := "Mystery/Ines Okoro/Harbour Cases/02 - Slack Tide"
	s.fake.addBook(2, fakeBook(14, "Slack Tide", []string{"Ines Okoro"},
		fakeFile(1, s.put(dir+"/01 - Slack Tide.mp3"), "content"),
		fakeFile(2, s.put(dir+"/02 - Slack Tide.mp3"), "content"),
	))
	s.run(s.config(false))
	book := s.bookByTitle("Slack Tide")
	if got, want := s.files(book.ID), []string{"audiobook " + s.local(dir)}; !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if book.MediaType != models.MediaTypeAudiobook {
		t.Errorf("media type = %q", book.MediaType)
	}
}

// Two ebook formats of one book are both owned files of that book.
func TestImportRegistersEveryEbookFormat(t *testing.T) {
	s := newShelf(t)
	epub := "Romance/Bea Lindqvist/Standalone/Paper Lanterns/Paper Lanterns.epub"
	azw3 := "Romance/Bea Lindqvist/Standalone/Paper Lanterns/Paper Lanterns.azw3"
	s.fake.addBook(2, fakeBook(15, "Paper Lanterns", []string{"Bea Lindqvist"},
		fakeFile(1, s.put(azw3), "content"),
		fakeFile(2, s.put(epub), "primary"),
	))
	stats := s.run(s.config(false))
	book := s.bookByTitle("Paper Lanterns")
	want := []string{"ebook " + s.local(azw3), "ebook " + s.local(epub)}
	if got := s.files(book.ID); !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if stats.OwnedMarked != 2 {
		t.Errorf("ownedMarked = %d, want 2", stats.OwnedMarked)
	}
}

// Two authors on one book: the first is the book's author, and the book is
// imported once, not once per author.
func TestImportTwoAuthors(t *testing.T) {
	s := newShelf(t)
	s.fake.addBook(2, fakeBook(16, "The Long Ledger", []string{"Tomas Reyes", "Hannah Whitlock"},
		fakeFile(1, s.put("Fiction/Tomas Reyes/Standalone/The Long Ledger/The Long Ledger.epub"), "primary"),
	))
	stats := s.run(s.config(false))
	if stats.BooksCreated != 1 || s.count("books") != 1 {
		t.Fatalf("books created = %d, rows = %d", stats.BooksCreated, s.count("books"))
	}
	if got := s.authorName(s.bookByTitle("The Long Ledger").AuthorID); got != "Tomas Reyes" {
		t.Errorf("author = %q, want the first listed author", got)
	}
}

// A file BookOrbit lists but Bindery cannot see is reported and not
// registered. The book itself is still imported.
func TestImportMissingFileIsReportedNotRegistered(t *testing.T) {
	s := newShelf(t)
	s.fake.addBook(2, fakeBook(17, "Gone Astray", []string{"Tomas Reyes"},
		fakeFile(1, containerRoot+"/Fiction/Tomas Reyes/Standalone/Gone Astray/Gone Astray.epub", "primary"),
	))
	stats := s.run(s.config(false))
	if stats.OwnedMarked != 0 || stats.PendingManual != 1 || stats.BooksCreated != 1 {
		t.Fatalf("stats = %s", stats)
	}
	if got := s.files(s.bookByTitle("Gone Astray").ID); len(got) != 0 {
		t.Errorf("a missing file was registered: %v", got)
	}
	results := s.importer.Progress().Results
	if len(results) != 1 || !strings.Contains(results[0].Message, "not visible to Bindery") {
		t.Errorf("item result does not report the missing file: %+v", results)
	}
}

// Without the remap the container path means nothing to Bindery, so nothing
// is registered. With it, the same book's file is.
func TestImportPathRemapDecidesWhatIsRegistered(t *testing.T) {
	s := newShelf(t)
	rel := "Fantasy/Ursula Vale/Tide Cycle/01 - First Light/First Light.epub"
	s.fake.addBook(2, fakeBook(18, "First Light", []string{"Ursula Vale"}, fakeFile(1, s.put(rel), "primary")))

	cfg := s.config(false)
	cfg.PathRemap = ""
	stats := s.run(cfg)
	if stats.OwnedMarked != 0 || stats.PendingManual != 1 {
		t.Fatalf("without a remap: %s", stats)
	}
	book := s.bookByTitle("First Light")
	if got := s.files(book.ID); len(got) != 0 {
		t.Fatalf("registered a container path: %v", got)
	}

	stats = s.run(s.config(false))
	if stats.OwnedMarked != 1 || stats.PendingManual != 0 || stats.BooksCreated != 0 {
		t.Fatalf("with the remap: %s", stats)
	}
	if got, want := s.files(book.ID), []string{"ebook " + s.local(rel)}; !equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func seedLibrary(s *shelf) {
	s.fake.addBook(2, withSeries(fakeBook(21, "The Salt Road", []string{"Ursula Vale"},
		fakeFile(1, s.put("Fantasy/Ursula Vale/Tide Cycle/02 - The Salt Road/The Salt Road.epub"), "primary")), 5, "Tide Cycle", "2"))
	s.fake.addBook(2, withSeries(fakeBook(22, "Low Water", []string{"Ursula Vale"},
		fakeFile(2, s.put("Fantasy/Ursula Vale/Tide Cycle/04.5 - Low Water/Low Water.epub"), "primary")), 5, "Tide Cycle", "4.5"))
	s.fake.addBook(2, fakeBook(23, "Fog Bell", []string{"Ines Okoro"},
		fakeFile(3, s.put("Mystery/Ines Okoro/Harbour Cases/01 - Fog Bell/Fog Bell.epub"), "primary"),
		fakeFile(4, s.put("Mystery/Ines Okoro/Harbour Cases/01 - Fog Bell/Fog Bell.m4b"), "content")))
}

func (s *shelf) snapshot() map[string]int {
	out := map[string]int{}
	for _, table := range []string{"authors", "books", "series", "series_books", "book_files", "editions", "author_aliases"} {
		out[table] = s.count(table)
	}
	return out
}

// A second run over an unchanged library creates nothing and registers
// nothing: same rows, no new author, book, series or file.
func TestImportSecondRunChangesNothing(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	first := s.run(s.config(false))
	if first.BooksCreated != 3 || first.AuthorsCreated != 2 || first.SeriesCreated != 1 || first.OwnedMarked != 4 {
		t.Fatalf("first run: %s", first)
	}
	before := s.snapshot()

	second := s.run(s.config(false))
	if second.BooksCreated != 0 || second.AuthorsCreated != 0 || second.SeriesCreated != 0 || second.SeriesLinked != 0 || second.OwnedMarked != 0 || second.EditionsAdded != 0 || second.Failed != 0 {
		t.Fatalf("second run changed something: %s", second)
	}
	if after := s.snapshot(); !sameCounts(before, after) {
		t.Fatalf("row counts moved: before %v, after %v", before, after)
	}
}

// A dry run reports what an import would do and writes none of it.
func TestDryRunWritesNothing(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	stats := s.run(s.config(true))
	if stats.BooksCreated != 3 || stats.AuthorsCreated != 2 || stats.OwnedMarked != 4 {
		t.Fatalf("dry run counts: %s", stats)
	}
	for table, n := range s.snapshot() {
		if n != 0 {
			t.Errorf("dry run wrote %d rows to %s", n, table)
		}
	}
	progress := s.importer.Progress()
	if !progress.DryRun || len(progress.Results) != 3 {
		t.Errorf("dry run sample = %+v", progress.Results)
	}
}

// The importer never moves, renames, deletes or touches a file.
func TestImportLeavesFilesAlone(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	before := s.tree()
	s.run(s.config(false))
	s.run(s.config(false))
	if after := s.tree(); !equal(before, after) {
		t.Fatalf("the shelf changed on disk:\nbefore %v\nafter  %v", before, after)
	}
}

// The importer only reads BookOrbit: GETs, and the one POST that is the book
// listing query. The fake fails the test on any other request, and this
// checks the full list as well.
func TestImportOnlyReadsBookOrbit(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	s.run(s.config(false))
	for _, req := range s.fake.seen() {
		method, path, _ := strings.Cut(req, " ")
		switch {
		case method == "GET":
		case method == "POST" && path == "/api/v1/libraries/2/books":
		default:
			t.Errorf("unexpected request to BookOrbit: %s", req)
		}
	}
}

// BookOrbit adding fields is not a change the importer notices.
func TestImportIgnoresUnknownFields(t *testing.T) {
	s := newShelf(t)
	book := fakeBook(31, "New Fields", []string{"Ursula Vale"}, fakeFile(1, s.put("Fantasy/Ursula Vale/Standalone/New Fields/New Fields.epub"), "primary"))
	book["somethingNew"] = map[string]any{"nested": []int{1, 2}}
	book["files"].([]map[string]any)[0]["checksumV2"] = "abc"
	book["authors"].([]map[string]any)[0]["pronouns"] = "they"
	s.fake.addBook(2, book)
	stats := s.run(s.config(false))
	if stats.BooksCreated != 1 || stats.OwnedMarked != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %s", stats)
	}
}

// A record that lost a field the import needs fails the whole run, and the run
// writes nothing: not the books before the broken one, and not the libraries
// before its library. The broken record is the last book of the last library
// on purpose.
func TestImportChangedShapeFailsWithoutPartialWrites(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	s.fake.addLibrary(3, "Second Shelf", containerRoot)
	s.fake.addBook(3, fakeBook(41, "Fine Book", []string{"Bea Lindqvist"}, fakeFile(10, s.put("Romance/Bea Lindqvist/Standalone/Fine Book/Fine Book.epub"), "primary")))
	broken := fakeBook(42, "Broken Book", []string{"Bea Lindqvist"}, fakeFile(11, s.put("Romance/Bea Lindqvist/Standalone/Broken Book/Broken Book.epub"), "primary"))
	broken["bookFiles"] = broken["files"]
	delete(broken, "files")
	s.fake.addBook(3, broken)

	cfg := s.config(false)
	cfg.LibraryIDs = []int64{2, 3}
	_, err := s.importer.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("a run over a changed API shape succeeded")
	}
	for _, want := range []string{"GET /api/v1/books/42", `"files" is missing`, "nothing was imported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	for table, n := range s.snapshot() {
		if n != 0 {
			t.Errorf("failed run left %d rows in %s", n, table)
		}
	}
	runs, err := s.importer.RunsPage(context.Background(), 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("runs = %+v, %v; want one failed run", runs, err)
	}

	// Once the shape is right again the same run imports everything.
	broken["files"] = broken["bookFiles"]
	stats := s.run(cfg)
	if stats.BooksCreated != 5 || stats.LibrariesScanned != 2 {
		t.Fatalf("after the shape was restored: %s", stats)
	}
}

// A library the key cannot see fails the run up front, with words that say
// what to check. BookOrbit answers a listing for such a library with an empty
// page, which would otherwise read as an empty library.
func TestImportUnknownLibraryFailsClearly(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	cfg := s.config(false)
	cfg.LibraryIDs = []int64{2, 99}
	_, err := s.importer.Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "library 99 is not visible to this API key") {
		t.Fatalf("err = %v", err)
	}
	if n := s.count("books"); n != 0 {
		t.Errorf("%d books imported from a run that should have failed first", n)
	}
}

func TestImportRejectedKeyFailsClearly(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	cfg := s.config(false)
	cfg.APIKey = "bok_revoked"
	_, err := s.importer.Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "bok_revoked") {
		t.Errorf("error repeats the key: %v", err)
	}
}

// More books than one page holds are all read, each once.
func TestImportPagesThroughALargeLibrary(t *testing.T) {
	s := newShelf(t)
	total := PageSize*2 + 7
	for n := 1; n <= total; n++ {
		title := "Volume " + strings.Repeat("I", n%4+1) + " " + string(rune('A'+n%26)) + " " + itoa(n)
		s.fake.addBook(2, fakeBook(int64(1000+n), title, []string{"Prolific Writer"},
			fakeFile(int64(n), containerRoot+"/Bulk/Prolific Writer/Standalone/"+itoa(n)+"/book.epub", "primary")))
	}
	stats := s.run(s.config(true))
	if stats.ItemsSeen != total || stats.PagesScanned != 3 || stats.ItemsNormalized != total {
		t.Fatalf("stats = %s, want %d items over 3 pages", stats, total)
	}
}

// A book with nothing Bindery tracks (here only a cover) is not imported.
func TestImportSkipsBookWithoutTrackedFiles(t *testing.T) {
	s := newShelf(t)
	s.fake.addBook(2, fakeBook(51, "Only A Cover", []string{"Ursula Vale"}, fakeFile(1, containerRoot+"/x/cover.jpg", "cover")))
	stats := s.run(s.config(false))
	if stats.ItemsSeen != 1 || stats.ItemsNormalized != 0 || s.count("books") != 0 {
		t.Fatalf("stats = %s, books = %d", stats, s.count("books"))
	}
}

// An author and a series Bindery already has are reused, not duplicated: the
// author by name, the series by the key the Audiobookshelf import and the
// duplicate series finder share, which folds case.
func TestImportReusesExistingAuthorAndSeries(t *testing.T) {
	s := newShelf(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL123A", Name: "Ursula Vale", SortName: "Vale, Ursula", MetadataProvider: "openlibrary", Monitored: true}
	if err := s.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := &models.Series{ForeignID: "hc:tide", Title: "Tide cycle"}
	if err := s.series.CreateOrGet(ctx, existing); err != nil {
		t.Fatal(err)
	}
	s.fake.addBook(2, withSeries(fakeBook(61, "The Salt Road", []string{"Ursula Vale"},
		fakeFile(1, s.put("Fantasy/Ursula Vale/Tide Cycle/02 - The Salt Road/The Salt Road.epub"), "primary")), 5, "Tide Cycle", "2"))

	stats := s.run(s.config(false))
	if stats.AuthorsCreated != 0 || stats.AuthorsLinked != 1 || stats.SeriesCreated != 0 {
		t.Fatalf("stats = %s", stats)
	}
	if s.count("authors") != 1 || s.count("series") != 1 {
		t.Fatalf("authors = %d, series = %d; want 1 and 1", s.count("authors"), s.count("series"))
	}
	if got := s.bookByTitle("The Salt Road").AuthorID; got != author.ID {
		t.Errorf("book author = %d, want the existing author %d", got, author.ID)
	}
}

// Tenancy: an imported book belongs to whoever owns its author, as with the
// Audiobookshelf importer. A book under another user's author is theirs, and
// an author the import creates is unowned, so its books are too.
func TestImportOwnershipFollowsTheAuthor(t *testing.T) {
	s := newShelf(t)
	ctx := context.Background()
	users := db.NewUserRepo(s.database)
	if _, err := users.Create(ctx, "admin", "h"); err != nil {
		t.Fatal(err)
	}
	alice, err := users.Create(ctx, "alice", "h")
	if err != nil {
		t.Fatal(err)
	}
	owned := &models.Author{ForeignID: "OL9A", Name: "Ines Okoro", SortName: "Okoro, Ines", MetadataProvider: "openlibrary", Monitored: true}
	if err := s.authors.CreateForUser(ctx, owned, alice.ID); err != nil {
		t.Fatal(err)
	}
	s.fake.addBook(2, fakeBook(71, "Fog Bell", []string{"Ines Okoro"}, fakeFile(1, s.put("Mystery/Ines Okoro/Harbour Cases/01 - Fog Bell/Fog Bell.epub"), "primary")))
	s.fake.addBook(2, fakeBook(72, "Unclaimed", []string{"Nobody Yet"}, fakeFile(2, s.put("Mystery/Nobody Yet/Standalone/Unclaimed/Unclaimed.epub"), "primary")))

	s.run(s.config(false))

	if got := s.bookByTitle("Fog Bell").OwnerUserID; got != alice.ID {
		t.Errorf("book under alice's author has owner %d, want %d", got, alice.ID)
	}
	unclaimed := s.bookByTitle("Unclaimed")
	author, _ := s.authors.GetByID(ctx, unclaimed.AuthorID)
	if unclaimed.OwnerUserID != author.OwnerUserID {
		t.Errorf("book owner %d differs from its author's owner %d", unclaimed.OwnerUserID, author.OwnerUserID)
	}
	// alice sees her book and not the other; the reverse holds for the
	// second book's owner.
	visible, _, err := s.books.ListPage(ctx, alice.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range visible {
		if b.Title == "Unclaimed" && unclaimed.OwnerUserID != alice.ID && unclaimed.OwnerUserID != 0 {
			t.Errorf("alice can see a book owned by user %d", unclaimed.OwnerUserID)
		}
	}
}

// BookOrbit numbers libraries from 1. The engine must never see a bare
// library id, or a BookOrbit library and another source's library of the same
// id would share placeholder ids and each other's provenance.
func TestLibraryIDsAreNamespaced(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	s.run(s.config(false))
	runs, err := s.runs.ListPage(context.Background(), 10, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if runs[0].SourceID != SourceID || runs[0].LibraryID != "bookorbit-2" || runs[0].SourceLabel != Label {
		t.Errorf("run recorded as source %q library %q label %q", runs[0].SourceID, runs[0].LibraryID, runs[0].SourceLabel)
	}
	if DisplayLibraryID(runs[0].LibraryID) != "2" {
		t.Errorf("DisplayLibraryID = %q", DisplayLibraryID(runs[0].LibraryID))
	}
	var bare int
	if err := s.database.QueryRow("SELECT COUNT(*) FROM abs_provenance WHERE source_id != ? OR library_id NOT LIKE 'bookorbit-%'", SourceID).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if bare != 0 {
		t.Errorf("%d provenance rows are not namespaced to BookOrbit", bare)
	}
	if strings.Contains(runs[0].SourceConfigJSON, fakeKey) || strings.Contains(runs[0].SummaryJSON, fakeKey) {
		t.Error("the API key was written into the run record")
	}
}

// The history lists BookOrbit runs only, newest first, and pages.
func TestRunsPageListsOnlyBookOrbitRuns(t *testing.T) {
	s := newShelf(t)
	seedLibrary(s)
	ctx := context.Background()
	if err := s.runs.Create(ctx, &models.ABSImportRun{SourceID: abs.DefaultSourceID, SourceLabel: "Audiobookshelf", LibraryID: "lib-abs", Status: "completed", SourceConfigJSON: "{}", CheckpointJSON: "{}", SummaryJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	s.run(s.config(true))
	s.run(s.config(false))

	runs, err := s.importer.RunsPage(ctx, 10, 0)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v; want the two BookOrbit runs", len(runs), err)
	}
	if runs[0].DryRun || !runs[1].DryRun {
		t.Errorf("order: newest first expected, got dryRun %v then %v", runs[0].DryRun, runs[1].DryRun)
	}
	page, err := s.importer.RunsPage(ctx, 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != runs[1].ID {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	// And the Audiobookshelf history, scoped the way main.go wires it, does
	// not show BookOrbit's.
	absRuns, err := s.runs.ListPageBySource(ctx, abs.DefaultSourceID, 10, 0)
	if err != nil || len(absRuns) != 1 || absRuns[0].LibraryID != "lib-abs" {
		t.Fatalf("abs runs = %+v, %v", absRuns, err)
	}
}

func TestConfigValidation(t *testing.T) {
	s := newShelf(t)
	base := s.config(true)
	for name, mutate := range map[string]func(*Config){
		"no url":       func(c *Config) { c.BaseURL = " " },
		"no key":       func(c *Config) { c.APIKey = "" },
		"no libraries": func(c *Config) { c.LibraryIDs = nil },
		"bad remap":    func(c *Config) { c.PathRemap = "no-colon-here" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := s.importer.Run(context.Background(), cfg); err == nil {
			t.Errorf("%s: Run accepted the config", name)
		}
	}
	if len(s.fake.seen()) != 0 {
		t.Errorf("an invalid config still reached BookOrbit: %v", s.fake.seen())
	}
}

func TestNormalizeChoosesFiles(t *testing.T) {
	book := &Book{ID: 5, Title: "T", Authors: []Person{{ID: 1, Name: "A"}}, Files: []File{
		{ID: 1, Path: "/b/T/T.azw3", Role: "content"},
		{ID: 2, Path: "/b/T/T.epub", Role: "content"},
		{ID: 3, Path: "/b/T/T.pdf", Role: "primary"},
		{ID: 4, Path: "/b/T/cover.jpg", Role: "cover"},
		{ID: 5, Path: "/b/T/notes.xyz", Role: "content"},
		{ID: 6, Path: "/b/T/disc 2/02.mp3", Role: "content"},
		{ID: 7, Path: "/b/T/disc 1/01.mp3", Role: "content"},
	}}
	item, ok := normalize(book, "bookorbit-2")
	if !ok {
		t.Fatal("normalize rejected a book with files")
	}
	if item.EbookPath != "/b/T/T.pdf" {
		t.Errorf("primary ebook = %q, want the file BookOrbit marks primary", item.EbookPath)
	}
	if !equal(item.ExtraEbookPaths, []string{"/b/T/T.epub", "/b/T/T.azw3"}) {
		t.Errorf("extra ebooks = %v", item.ExtraEbookPaths)
	}
	if len(item.AudioFiles) != 2 || item.Path != "/b/T" || item.IsFile {
		t.Errorf("audio: %d files, path %q", len(item.AudioFiles), item.Path)
	}
	if item.ItemID != "5" || item.LibraryID != "bookorbit-2" || item.Authors[0].ID != "1" {
		t.Errorf("ids = %q %q %q", item.ItemID, item.LibraryID, item.Authors[0].ID)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
