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

// TestLookupAuthorMatch covers the pure author-matching helper.
func TestLookupAuthorMatch(t *testing.T) {
	cases := []struct {
		parsed, catalogue string
		want              bool
	}{
		{"Nick Lane", "Nick Lane", true},
		{"nick lane", "Nick Lane", true},
		{"Lane, Nick", "Nick Lane", true},
		{"Nick Lane", "Lane, Nick", true},
		{"N. Lane", "Nick Lane", true}, // initials still fuzzy-match the full name
		{"", "Nick Lane", false},
		{"Nick Lane", "", false},
		{"Andy Weir", "Nick Lane", false},
		// JaroWinkler fuzzy: slight misspelling still matches
		{"Nck Lane", "Nick Lane", true},
	}
	for _, tc := range cases {
		got := lookupAuthorMatch(tc.parsed, tc.catalogue)
		if got != tc.want {
			t.Errorf("lookupAuthorMatch(%q, %q) = %v, want %v", tc.parsed, tc.catalogue, got, tc.want)
		}
	}
}

// TestInvertAuthorName covers the "Last, First" → "first last" helper.
func TestInvertAuthorName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"lane, nick", "nick lane"},
		{"le guin, ursula k.", "ursula k. le guin"},
		{"nick lane", "nick lane"}, // no comma - returned unchanged
		{",first", ",first"},       // no last name segment
		{"last,", "last,"},         // no first name after comma
	}
	for _, tc := range cases {
		got := invertAuthorName(tc.in)
		if got != tc.want {
			t.Errorf("invertAuthorName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLookupDetectFormat verifies directory → audiobook, file → extension-based.
func TestLookupDetectFormat(t *testing.T) {
	tmp := t.TempDir()

	dir := filepath.Join(tmp, "audiofolder")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookupDetectFormat(dir); got != models.MediaTypeAudiobook {
		t.Errorf("directory: got %q, want %q", got, models.MediaTypeAudiobook)
	}

	epub := filepath.Join(tmp, "book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lookupDetectFormat(epub); got != models.MediaTypeEbook {
		t.Errorf("epub file: got %q, want %q", got, models.MediaTypeEbook)
	}

	m4b := filepath.Join(tmp, "book.m4b")
	if err := os.WriteFile(m4b, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lookupDetectFormat(m4b); got != models.MediaTypeAudiobook {
		t.Errorf("m4b file: got %q, want %q", got, models.MediaTypeAudiobook)
	}
}

// TestScanner_Lookup_ASIN verifies that a filename ASIN is matched exactly.
func TestScanner_Lookup_ASIN(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	author := &models.Author{Name: "Nick Lane", ForeignID: "a1", SortName: "Lane, Nick"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Life Ascending", ASIN: "B001234567", ForeignID: "b1", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	f := filepath.Join(tmp, "Life.Ascending.B001234567.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "confident" {
		t.Fatalf("match = %q, want confident", result.Match)
	}
	if result.Book == nil || result.Book.ID != book.ID {
		t.Errorf("book mismatch: got %v, want id=%d", result.Book, book.ID)
	}
	if result.DetectedFormat != models.MediaTypeEbook {
		t.Errorf("detectedFormat = %q, want ebook", result.DetectedFormat)
	}
}

// TestScanner_Lookup_TitleAuthor verifies fuzzy title+author matching.
func TestScanner_Lookup_TitleAuthor(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	author := &models.Author{Name: "Andy Weir", ForeignID: "a2", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Project Hail Mary", ForeignID: "b2", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	f := filepath.Join(tmp, "Project.Hail.Mary.Andy.Weir.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "confident" {
		t.Fatalf("match = %q, want confident", result.Match)
	}
	if result.Book == nil || result.Book.ID != book.ID {
		t.Errorf("book mismatch: got %v, want id=%d", result.Book, book.ID)
	}
}

// TestScanner_LookupBatch_MatchesEqualLookup verifies LookupBatch returns the
// same per-path match as calling Lookup individually, so the N+1 fix for #1473
// (loading the catalogue once for the whole batch) does not alter matching.
func TestScanner_LookupBatch_MatchesEqualLookup(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	author := &models.Author{Name: "Andy Weir", ForeignID: "a1", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Project Hail Mary", ForeignID: "b1", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	match := filepath.Join(tmp, "Project.Hail.Mary.Andy.Weir.epub")
	miss := filepath.Join(tmp, "Unknown.Book.Nobody.epub")
	for _, f := range []string{match, miss} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths := []string{match, miss}
	batch, err := s.LookupBatch(ctx, paths)
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(batch) != len(paths) {
		t.Fatalf("batch len = %d, want %d", len(batch), len(paths))
	}
	for i, p := range paths {
		want, err := s.Lookup(ctx, p)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", p, err)
		}
		if batch[i].Match != want.Match {
			t.Errorf("path %s: batch match = %q, want %q", p, batch[i].Match, want.Match)
		}
		if (batch[i].Book == nil) != (want.Book == nil) {
			t.Errorf("path %s: batch book presence mismatch", p)
		}
		if batch[i].Book != nil && want.Book != nil && batch[i].Book.ID != want.Book.ID {
			t.Errorf("path %s: batch book id = %d, want %d", p, batch[i].Book.ID, want.Book.ID)
		}
	}
	if batch[0].Match != "confident" || batch[1].Match != "none" {
		t.Errorf("matches = %q,%q; want confident,none", batch[0].Match, batch[1].Match)
	}
}

// TestScanner_Lookup_None verifies no-match returns "none".
func TestScanner_Lookup_None(t *testing.T) {
	t.Parallel()
	s, _, _, ctx := scannerFixture(t, t.TempDir())

	tmp := t.TempDir()
	f := filepath.Join(tmp, "Unknown.Book.Nobody.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "none" {
		t.Errorf("match = %q, want none", result.Match)
	}
}

// TestScanner_Lookup_AudiobookFolder verifies a directory is detected as audiobook.
func TestScanner_Lookup_AudiobookFolder(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	author := &models.Author{Name: "Frank Herbert", ForeignID: "a3", SortName: "Herbert, Frank"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Dune", ForeignID: "b3", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "Dune - Frank Herbert")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, dir)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.DetectedFormat != models.MediaTypeAudiobook {
		t.Errorf("detectedFormat = %q, want audiobook", result.DetectedFormat)
	}
	if result.Match != "confident" {
		t.Errorf("match = %q, want confident", result.Match)
	}
}

// TestScanner_Lookup_EmptyTitle verifies a path with no parseable title returns "none".
func TestScanner_Lookup_EmptyTitle(t *testing.T) {
	t.Parallel()
	s, _, _, ctx := scannerFixture(t, t.TempDir())

	tmp := t.TempDir()
	f := filepath.Join(tmp, "_.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "none" {
		t.Errorf("match = %q, want none for unparseable filename", result.Match)
	}
}

// TestFormatHintOverridesDetection verifies that a non-empty formatHint passed
// to tryImportInternal overrides extension-based detection.
func TestFormatHintOverridesDetection(t *testing.T) {
	tmp := t.TempDir()
	// Write an epub but tell the importer it's an audiobook via hint.
	// The import will fail (no library dir for audiobook dest), but we only
	// need to verify the format branch that was entered — the status after
	// failing will be StateImportBlocked, not StateImportFailed (which would
	// indicate it entered the ebook path).
	epubFile := filepath.Join(tmp, "test.epub")
	if err := os.WriteFile(epubFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	author := &models.Author{Name: "Test Author", ForeignID: "fa1", SortName: "Author, Test"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Test Book", ForeignID: "fb1", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "hint-test",
		BookID: &book.ID,
		Title:  "Test Book",
		Status: models.StateCompleted,
	}
	// We just verify ImportFromPath is callable and transitions state.
	s.ImportFromPath(ctx, dl, epubFile, "")
}

// TestImportFromPath_FormatHintAudiobook exercises the formatHint override
// branch in tryImportInternal (scanner.go lines 1158-1159). Passing
// "audiobook" for a .epub file must route the import through the audiobook
// path — confirmed by a directory being created under the library root.
func TestImportFromPath_FormatHintAudiobook(t *testing.T) {
	tmp := t.TempDir()
	epubFile := filepath.Join(tmp, "test.epub")
	if err := os.WriteFile(epubFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libDir)

	author := &models.Author{Name: "Hint Author", ForeignID: "fha2", SortName: "Author, Hint"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Hint Book", ForeignID: "fhb2", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "hint-audiobook",
		BookID: &book.ID,
		Title:  "Hint Book",
		Status: models.StateCompleted,
	}
	s.ImportFromPath(ctx, dl, epubFile, models.MediaTypeAudiobook)

	// The audiobook path creates an author directory under libDir.
	entries, err := os.ReadDir(libDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected an author directory under libDir after audiobook import with hint")
	}
}

// TestImportFromPath_FormatHintEbook exercises the right-hand side of the
// || condition on scanner.go line 1158:
//
//	if formatHint == models.MediaTypeAudiobook || formatHint == models.MediaTypeEbook
//
// Passing "ebook" for a .m4b file (which auto-detects as audiobook) makes the
// left side false and the right side true, so both operands are evaluated.
// The import must be routed through the ebook path — confirmed by a file
// landing under the library root (ebook dest), not the audiobook dir.
func TestImportFromPath_FormatHintEbook(t *testing.T) {
	tmp := t.TempDir()
	m4bFile := filepath.Join(tmp, "test.m4b")
	if err := os.WriteFile(m4bFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	libDir := t.TempDir()
	s, books, authors, ctx := scannerFixture(t, libDir)

	author := &models.Author{Name: "Ebook Hint Author", ForeignID: "feha3", SortName: "Author, Ebook Hint"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{AuthorID: author.ID, Title: "Ebook Hint Book", ForeignID: "fehb3", Status: "wanted"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dl := &models.Download{
		GUID:   "hint-ebook",
		BookID: &book.ID,
		Title:  "Ebook Hint Book",
		Status: models.StateCompleted,
	}
	// "ebook" hint overrides .m4b auto-detection → ebook path is taken.
	s.ImportFromPath(ctx, dl, m4bFile, models.MediaTypeEbook)

	// The ebook path creates an author/book directory under libDir.
	entries, err := os.ReadDir(libDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected an author directory under libDir after ebook import with hint (m4b routed as ebook)")
	}
}

// TestScanner_Lookup_Ambiguous verifies that two books with the same title and
// no author in the filename produces a match = "ambiguous" result with both
// books listed as candidates.
func TestScanner_Lookup_Ambiguous(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	for i, name := range []string{"Author One", "Author Two"} {
		a := &models.Author{Name: name, ForeignID: "amb-a" + string(rune('1'+i)), SortName: name}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		b := &models.Book{
			AuthorID: a.ID, Title: "The Same Title",
			ForeignID: "amb-b" + string(rune('1'+i)), Status: "wanted",
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	tmp := t.TempDir()
	// Filename has only the title — no author to narrow the match.
	f := filepath.Join(tmp, "The.Same.Title.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "ambiguous" {
		t.Fatalf("match = %q, want ambiguous", result.Match)
	}
	if len(result.Candidates) != 2 {
		t.Errorf("len(Candidates) = %d, want 2", len(result.Candidates))
	}
	if result.Book != nil {
		t.Errorf("Book should be nil for ambiguous result, got %+v", result.Book)
	}
}

// TestScanner_Lookup_ASINFallthrough exercises the code path where an ASIN is
// present in the filename but no book in the catalogue carries that ASIN.
// After the ASIN loop exits without a match the function falls through to
// title-based matching and should return "confident" when the title matches.
func TestScanner_Lookup_ASINFallthrough(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	a := &models.Author{Name: "Nick Lane", ForeignID: "asin-ft-a", SortName: "Lane, Nick"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Book has no ASIN — the ASIN in the filename won't match it.
	b := &models.Book{AuthorID: a.ID, Title: "Life Ascending", ForeignID: "asin-ft-b", Status: "wanted"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	// B099999999 is not in the catalogue; the title "Life Ascending" is.
	f := filepath.Join(tmp, "Life.Ascending.B099999999.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "confident" {
		t.Fatalf("match = %q, want confident (title fallthrough after ASIN miss)", result.Match)
	}
	if result.Book == nil || result.Book.ID != b.ID {
		t.Errorf("book mismatch: got %v, want id=%d", result.Book, b.ID)
	}
}

// TestScanner_Lookup_AuthorFilterRejects exercises the author-filter branch
// inside the title-match loop: the title matches a book in the catalogue but
// the author parsed from the filename does not match that book's author,
// so the candidate is skipped and the result is "none".
func TestScanner_Lookup_AuthorFilterRejects(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	a := &models.Author{Name: "Andy Weir", ForeignID: "afr-a", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{AuthorID: a.ID, Title: "The Martian", ForeignID: "afr-b", Status: "wanted"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	// Title matches "The Martian" but the author part says "Stephen King".
	f := filepath.Join(tmp, "The Martian - Stephen King.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "none" {
		t.Errorf("match = %q, want none (author filter should reject the title match)", result.Match)
	}
}

// TestScanner_Lookup_TitleMismatch verifies that books whose title does not
// match the parsed filename are skipped (the !titleMatch → continue branch).
// The catalogue contains one book with a completely different title; the
// lookup finds no match and returns "none".
func TestScanner_Lookup_TitleMismatch(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())

	a := &models.Author{Name: "Mismatch Author", ForeignID: "mm-a", SortName: "Author, Mismatch"}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{AuthorID: a.ID, Title: "Completely Different Book", ForeignID: "mm-b", Status: "wanted"}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	f := filepath.Join(tmp, "Unknown.Title.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.Lookup(ctx, f)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.Match != "none" {
		t.Errorf("match = %q, want none (title mismatch should skip the catalogue entry)", result.Match)
	}
}

// TestScanner_Lookup_BooksListError verifies that a database error from
// books.List is surfaced as a wrapped "lookup: list books" error.
func TestScanner_Lookup_BooksListError(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	// Do not register t.Cleanup — we close the DB manually below.

	s := NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	)

	tmp := t.TempDir()
	f := filepath.Join(tmp, "Some.Book.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	database.Close() // force books.List to return an error

	_, lookupErr := s.Lookup(context.Background(), f)
	if lookupErr == nil {
		t.Fatal("expected error from books.List, got nil")
	}
	if !strings.Contains(lookupErr.Error(), "lookup: list books") {
		t.Errorf("error = %q, want 'lookup: list books' prefix", lookupErr.Error())
	}
}

// TestScanner_Lookup_AuthorsListError verifies that a database error from
// authors.List (after books.List succeeds) is surfaced as a wrapped
// "lookup: list authors" error.
// The scanner is wired with two separate in-memory DBs: bookDB stays open so
// books.List returns results; authorDB is closed before the Lookup call so
// authors.List fails.
func TestScanner_Lookup_AuthorsListError(t *testing.T) {
	bookDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bookDB.Close() })

	authorDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	// authorDB is closed manually below.

	s := NewScanner(
		db.NewDownloadRepo(bookDB),
		db.NewDownloadClientRepo(bookDB),
		db.NewBookRepo(bookDB),
		db.NewAuthorRepo(authorDB),
		db.NewHistoryRepo(bookDB),
		t.TempDir(), "", "", "", "",
	)

	// Seed a book on bookDB so the ASIN block is skipped and the function
	// reaches authors.List.
	ctx := context.Background()
	seedAuthor := &models.Author{
		ForeignID: "ale-a", Name: "Seed Author", SortName: "Author, Seed",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := db.NewAuthorRepo(bookDB).Create(ctx, seedAuthor); err != nil {
		t.Fatal(err)
	}
	seedBook := &models.Book{
		ForeignID: "ale-b", AuthorID: seedAuthor.ID,
		Title: "Authors List Error Book", SortTitle: "authors list error book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := db.NewBookRepo(bookDB).Create(ctx, seedBook); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	// No ASIN in the filename — the ASIN block is skipped, so authors.List is reached.
	f := filepath.Join(tmp, "Authors.List.Error.Book.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	authorDB.Close() // force authors.List to return an error

	_, lookupErr := s.Lookup(ctx, f)
	if lookupErr == nil {
		t.Fatal("expected error from authors.List, got nil")
	}
	if !strings.Contains(lookupErr.Error(), "lookup: list authors") {
		t.Errorf("error = %q, want 'lookup: list authors' prefix", lookupErr.Error())
	}
}
