package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_SeriesPositionReconcileTier covers the fourth and only
// untested reconcile tier in ScanLibrary: the series+position match
// (scanner.go ~1978, the s.series.GetBookBySeriesPosition path). It fires only
// when the ASIN and the fuzzy title+author tiers have both missed but the
// parsed filename yields a Series + SeriesNumber that resolve to a wanted book
// linked at that position.
//
// The fixture is built so ONLY the series tier can match:
//   - The file is an .epub (no audio tags), so the ASIN tier never runs —
//     parsed.ASIN stays empty.
//   - The file lives directly in the library root (not under an <Author>/
//     subfolder), so authorTitleFromLayout returns ok=false and the parsed
//     author/title come purely from the filename: "Brandon Sanderson" /
//     "The Way of Kings".
//   - The seeded wanted book is titled "Quantum Gardens" by "Ursula North" —
//     neither the parsed title nor author come anywhere near it, so the
//     fuzzy title tier (Jaro-Winkler >= 0.85) cannot match.
//   - That same book is linked to the series "Stormlight Archive" at
//     position "1", which is exactly what the filename's
//     "[Stormlight Archive, Book 1]" annotation parses to — so the series
//     tier is the one that attaches the file.
func TestScanLibrary_SeriesPositionReconcileTier(t *testing.T) {
	libDir := t.TempDir()

	// File sits directly in the library root so the folder-layout override does
	// not supply an author/title; parsing is filename-driven and yields
	// Series="Stormlight Archive", SeriesNumber="1", Title="The Way of Kings",
	// Author="Brandon Sanderson" — none of which match the seeded book except
	// the series position.
	epub := filepath.Join(libDir, "[Stormlight Archive, Book 1] The Way of Kings - Brandon Sanderson.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sanity-check the parse so the test fails loudly if parser behaviour drifts
	// out from under the fixture.
	parsed := ParseFilename(epub)
	if parsed.Series != "Stormlight Archive" || parsed.SeriesNumber != "1" {
		t.Fatalf("fixture parse: got Series=%q num=%q, want Stormlight Archive / 1", parsed.Series, parsed.SeriesNumber)
	}
	if parsed.ASIN != "" {
		t.Fatalf("fixture parse: ASIN must be empty so the ASIN tier is skipped, got %q", parsed.ASIN)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	series := db.NewSeriesRepo(database)
	s := NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database),
		libDir, "", "", "", "",
	).WithSeriesRepo(series)

	ctx := context.Background()

	// Author and title deliberately disjoint from the parsed filename so the
	// ASIN and fuzzy title+author tiers cannot fire — only the series tier can.
	author := &models.Author{ForeignID: "ol:ursula-north", Name: "Ursula North", SortName: "North, Ursula"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "ol:quantum-gardens", AuthorID: author.ID,
		Title: "Quantum Gardens", SortTitle: "Quantum Gardens",
		Status: models.BookStatusWanted, Monitored: true, Genres: []string{},
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	ser := &models.Series{ForeignID: "manual:stormlight", Title: "Stormlight Archive"}
	if err := series.Create(ctx, ser); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, ser.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	// The series tier should have attached the file to the wanted book via
	// AddBookFile. Assert on book_files (the authoritative record) and on the
	// refreshed status/FilePath.
	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("series tier: expected 1 book_file, got %d (%+v)", len(files), files)
	}
	if files[0].Path != epub {
		t.Errorf("series tier: book_file path = %q, want %q", files[0].Path, epub)
	}

	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != epub {
		t.Errorf("series tier: book FilePath = %q, want %q", got.FilePath, epub)
	}
	if got.Status == models.BookStatusWanted {
		t.Errorf("series tier: book status still %q, expected it to advance after the file was attached", got.Status)
	}
}
