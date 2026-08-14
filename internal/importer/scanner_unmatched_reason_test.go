package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// unmatchedReasonPayload reads back just the unmatched entries of the persisted
// scan result, including the #1958 reason field.
type unmatchedReasonPayload struct {
	Unmatched      int             `json:"unmatched"`
	UnmatchedFiles []unmatchedFile `json:"unmatched_files"`
}

func readUnmatchedFiles(t *testing.T, ctx context.Context, settings *db.SettingsRepo) []unmatchedFile {
	t.Helper()
	setting, err := settings.Get(ctx, "library.lastScan")
	if err != nil {
		t.Fatalf("get library.lastScan: %v", err)
	}
	if setting == nil {
		t.Fatal("expected library.lastScan to be persisted, got nil")
	}
	var p unmatchedReasonPayload
	if err := json.Unmarshal([]byte(setting.Value), &p); err != nil {
		t.Fatalf("unmarshal scan result %q: %v", setting.Value, err)
	}
	return p.UnmatchedFiles
}

// scanOneUnmatched lays a single epub at rel under the library dir, scans, and
// returns the one unmatched entry the scan must have produced.
func scanOneUnmatched(t *testing.T, s *Scanner, settings *db.SettingsRepo, ctx context.Context, libraryDir string, rel ...string) unmatchedFile {
	t.Helper()
	path := filepath.Join(append([]string{libraryDir}, rel...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, path, "", "", "")

	s.ScanLibrary(ctx)

	files := readUnmatchedFiles(t, ctx, settings)
	if len(files) != 1 {
		t.Fatalf("expected exactly one unmatched file, got %d (%+v)", len(files), files)
	}
	return files[0]
}

// TestScanLibrary_UnmatchedReasonAuthorNotInLibrary is the #1958 regression: a
// file whose parsed author matches NO author in the library must say so. The UI
// used to answer this case with "refresh the author's book catalogue", which
// cannot help — there is no such author to refresh — and it pointed away from
// the evidence (the parsed author) for two weeks.
func TestScanLibrary_UnmatchedReasonAuthorNotInLibrary(t *testing.T) {
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:phm", AuthorID: author.ID, Title: "Project Hail Mary", SortTitle: "project hail mary",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	got := scanOneUnmatched(t, s, settings, ctx, libraryDir, "Becky Chambers", "A Psalm for the Wild-Built", "A Psalm for the Wild-Built.epub")
	if got.Reason != unmatchedReasonAuthorNotInLibrary {
		t.Errorf("reason = %q, want %q", got.Reason, unmatchedReasonAuthorNotInLibrary)
	}
	if got.ParsedAuthor != "Becky Chambers" {
		t.Errorf("parsed author = %q, want %q — the hint names it, so it has to be the real value", got.ParsedAuthor, "Becky Chambers")
	}
}

// TestScanLibrary_UnmatchedReasonNoCandidateBooks keeps the ORIGINAL advice
// pointed at the case it was written for (#875): the author is in the library
// but has no book the scan can attach a file to.
func TestScanLibrary_UnmatchedReasonNoCandidateBooks(t *testing.T) {
	s, _, _, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	got := scanOneUnmatched(t, s, settings, ctx, libraryDir, "Andy Weir", "Project Hail Mary", "Project Hail Mary.epub")
	if got.Reason != unmatchedReasonNoCandidateBooks {
		t.Errorf("reason = %q, want %q", got.Reason, unmatchedReasonNoCandidateBooks)
	}
}

// TestScanLibrary_UnmatchedReasonNoTitleMatch covers the third case: the author
// matched and has wanted books, but none of their titles is this file.
func TestScanLibrary_UnmatchedReasonNoTitleMatch(t *testing.T) {
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:phm", AuthorID: author.ID, Title: "Project Hail Mary", SortTitle: "project hail mary",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	got := scanOneUnmatched(t, s, settings, ctx, libraryDir, "Andy Weir", "The Martian", "The Martian.epub")
	if got.Reason != unmatchedReasonNoTitleMatch {
		t.Errorf("reason = %q, want %q", got.Reason, unmatchedReasonNoTitleMatch)
	}
}

// TestScanLibrary_UnmatchedReasonNoTitleParsed covers the fourth case: nothing
// in the path parsed as a title, so the title tier never ran. Reporting
// no_title_match here told the user "no book by that author matched this title"
// about a file that had no title to match — the fix is to rename the file, and
// the reason has to say that.
func TestScanLibrary_UnmatchedReasonNoTitleParsed(t *testing.T) {
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:phm", AuthorID: author.ID, Title: "Project Hail Mary", SortTitle: "project hail mary",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// A name that is nothing but a stripped parenthetical year parses to an
	// empty title, which is exactly the shape a release dump leaves behind.
	got := scanOneUnmatched(t, s, settings, ctx, libraryDir, "Andy Weir", "(2021).epub")
	if got.Reason != unmatchedReasonNoTitleParsed {
		t.Errorf("reason = %q, want %q", got.Reason, unmatchedReasonNoTitleParsed)
	}
}

// TestScanLibrary_UnmatchedReasonUsesTheAuthorFallback ties #1958 back to
// #1956: a contributor-list tag whose author IS in the library must not be
// reported as "author not in library" just because the raw tag string can't
// match. The reason has to be computed from the same resolution the matcher
// used, so this file is reported as a title miss.
func TestScanLibrary_UnmatchedReasonUsesTheAuthorFallback(t *testing.T) {
	s, _, books, authors, settings, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:enrigue", Name: "Álvaro Enrigue", SortName: "Enrigue, Álvaro", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:sudden", AuthorID: author.ID, Title: "Sudden Death", SortTitle: "sudden death",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(libraryDir, "Álvaro Enrigue", "You Dreamed of Empires")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m4b := filepath.Join(dir, "You Dreamed of Empires.m4b")
	if err := os.WriteFile(m4b, buildID3v23("You Dreamed of Empires", daizeArtistTag, ""), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	files := readUnmatchedFiles(t, ctx, settings)
	if len(files) != 1 {
		t.Fatalf("expected exactly one unmatched file, got %d (%+v)", len(files), files)
	}
	if files[0].Reason != unmatchedReasonNoTitleMatch {
		t.Errorf("reason = %q, want %q", files[0].Reason, unmatchedReasonNoTitleMatch)
	}
}
