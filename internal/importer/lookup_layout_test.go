package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedLayoutBook inserts an author + book (optionally with ISBNs) and returns the book.
func seedLayoutBook(t *testing.T, books *db.BookRepo, authors *db.AuthorRepo, ctx context.Context, authorName, title string, isbns ...string) *models.Book {
	t.Helper()
	a := &models.Author{Name: authorName, ForeignID: "la-" + authorName, SortName: authorName}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{AuthorID: a.ID, Title: title, ForeignID: "lb-" + title, Status: "wanted", ISBNs: isbns}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLookupBatchLayout_FolderAuthorConfirmsTitle is the core #1434 case: the
// filename carries only the title, and the AUTHOR comes from the folder layout
// (<root>/<Author>/<file>). The folder author corroborates the single title
// match, so it is confident — no more "no catalogue match" for a classic
// Author/Title.epub library.
func TestLookupBatchLayout_FolderAuthorConfirmsTitle(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	p := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (folder author corroborates)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
	if res[0].ParsedAuthor != "Andy Weir" {
		t.Errorf("ParsedAuthor = %q, want folder-derived 'Andy Weir'", res[0].ParsedAuthor)
	}
}

// TestLookupBatchLayout_EmbeddedMetadataWins verifies embedded EPUB metadata
// beats a misleading filename and folder: the file name is garbage and the
// folder author is wrong, but the embedded title+author match the catalogue.
func TestLookupBatchLayout_EmbeddedMetadataWins(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	p := filepath.Join(root, "Wrong Author", "book_final_v2.epub")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, p, "Project Hail Mary", "Andy Weir", "")

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (embedded metadata)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
}

// TestLookupBatchLayout_DemotesLooseSingleMatch is the #1402 box-set fix: a lone
// loose (non-exact, token-overlap) title match with NO author signal must NOT be
// auto-applied as confident. It is demoted to ambiguous so the wizard asks.
func TestLookupBatchLayout_DemotesLooseSingleMatch(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	seedLayoutBook(t, books, authors, ctx, "Cal Newport", "Deep Work")

	root := t.TempDir()
	// File directly under root → no folder author; title only loosely overlaps.
	p := filepath.Join(root, "Deep Work Boxed Set Collection.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "ambiguous" {
		t.Fatalf("match = %q, want ambiguous (loose single match, no author)", res[0].Match)
	}
	if len(res[0].Candidates) != 1 {
		t.Errorf("candidates = %d, want 1 (the demoted match surfaced for review)", len(res[0].Candidates))
	}
	if res[0].Book != nil {
		t.Errorf("Book should be nil for a demoted ambiguous match, got %+v", res[0].Book)
	}
}

// TestLookupBatchLayout_ExactTitleNoAuthorStaysConfident guards the demotion
// from over-firing: an EXACT title match with no author is still confident.
func TestLookupBatchLayout_ExactTitleNoAuthorStaysConfident(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	book := seedLayoutBook(t, books, authors, ctx, "Frank Herbert", "Dune")

	root := t.TempDir()
	p := filepath.Join(root, "Dune.epub")
	writeFileAt(t, p)

	res, err := s.LookupBatchLayout(ctx, root, []string{p})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if res[0].Match != "confident" {
		t.Fatalf("match = %q, want confident (exact title)", res[0].Match)
	}
	if res[0].Book == nil || res[0].Book.ID != book.ID {
		t.Errorf("book = %v, want id=%d", res[0].Book, book.ID)
	}
}

// TestLookupBatchLayout_LoadsCatalogueOnce is the #1473 regression guard mirrored
// for the layout path: many paths, one catalogue load, results aligned.
func TestLookupBatchLayout_LoadsCatalogueOnce(t *testing.T) {
	t.Parallel()
	s, books, authors, ctx := scannerFixture(t, t.TempDir())
	seedLayoutBook(t, books, authors, ctx, "Andy Weir", "Project Hail Mary")

	root := t.TempDir()
	match := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	miss := filepath.Join(root, "Nobody", "Unknown Book.epub")
	writeFileAt(t, match)
	writeFileAt(t, miss)

	res, err := s.LookupBatchLayout(ctx, root, []string{match, miss})
	if err != nil {
		t.Fatalf("LookupBatchLayout: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d, want 2 aligned with inputs", len(res))
	}
	if res[0].Match != "confident" || res[1].Match != "none" {
		t.Errorf("matches = %q,%q; want confident,none", res[0].Match, res[1].Match)
	}
}

// TestDetectUnitFormat covers the format-by-contents fix (#1434): a directory is
// no longer blindly "audiobook" — an all-ebook folder is ebook, an audio folder
// is audiobook, and files go by extension.
func TestDetectUnitFormat(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	ebookDir := filepath.Join(tmp, "ebook-folder")
	writeFileAt(t, filepath.Join(ebookDir, "Title.epub"))
	writeFileAt(t, filepath.Join(ebookDir, "Title.mobi"))
	if got := detectUnitFormat(ebookDir); got != models.MediaTypeEbook {
		t.Errorf("all-ebook dir: got %q, want ebook", got)
	}

	audioDir := filepath.Join(tmp, "audio-folder")
	writeFileAt(t, filepath.Join(audioDir, "CD1", "01.mp3"))
	if got := detectUnitFormat(audioDir); got != models.MediaTypeAudiobook {
		t.Errorf("audio dir: got %q, want audiobook", got)
	}

	epub := filepath.Join(tmp, "loose.epub")
	writeFileAt(t, epub)
	if got := detectUnitFormat(epub); got != models.MediaTypeEbook {
		t.Errorf("epub file: got %q, want ebook", got)
	}

	m4b := filepath.Join(tmp, "loose.m4b")
	writeFileAt(t, m4b)
	if got := detectUnitFormat(m4b); got != models.MediaTypeAudiobook {
		t.Errorf("m4b file: got %q, want audiobook", got)
	}
}
