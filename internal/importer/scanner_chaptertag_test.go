package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_ChapterTaggedAudiobookReconciles is the end-to-end #1239
// regression: a multi-part audiobook whose tracks carry per-chapter title tags
// and a "Read by …" narrator artist must still reconcile to the catalogue book
// via the Author/Book folder hierarchy, instead of every track landing in
// Unmatched. It drives the real ScanLibrary path (ReadAudioTags reads the file
// on disk), so it would catch the layoutTitle/tag wiring being inverted.
func TestScanLibrary_ChapterTaggedAudiobookReconciles(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)

	author := &models.Author{ForeignID: "ol:pratchett", Name: "Terry Pratchett", SortName: "Pratchett, Terry", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:sourcery", AuthorID: author.ID, Title: "Sourcery", SortTitle: "sourcery",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Lay the track out as <library>/Terry Pratchett/Sourcery/<track>.mp3, tagged
	// with a chapter title and a narrator credit in the Artist field — exactly
	// the shape that previously poisoned the match.
	dir := filepath.Join(libraryDir, "Terry Pratchett", "Sourcery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	trackPath := filepath.Join(dir, "dw05_04.mp3")
	tagged := buildID3v23("04 - Sinister Grey Mists Rolled Thru Docks Of Morpork", "Read by Nigel Planer", "")
	if err := os.WriteFile(trackPath, tagged, 0o644); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	paths, err := books.ListAllBookFilePaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range paths {
		if filepath.Clean(p) == filepath.Clean(trackPath) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("chapter-tagged track was not reconciled to the catalogue book; book_file paths = %v", paths)
	}
}
