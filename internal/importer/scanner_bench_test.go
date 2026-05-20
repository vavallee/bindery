package importer

// Benchmarks for the library-scan hot path.
//
// ScanLibrary reconciles every untracked file on disk against the "wanted"
// books in the database. These benchmarks measure that scaling for two
// library layouts and isolate the per-comparison primitives.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// silenceSlog discards log output for the duration of a benchmark — ScanLibrary
// logs two lines per call and bindery routes slog to stdout, which otherwise
// drowns the benchmark results. Restored on cleanup.
func silenceSlog(tb testing.TB) {
	tb.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tb.Cleanup(func() { slog.SetDefault(prev) })
}

// benchScannerFixture mirrors scannerFixture (scanner_extra_test.go) but accepts
// testing.TB so benchmarks can share it.
func benchScannerFixture(tb testing.TB, libraryDir string) (*Scanner, *db.BookRepo, *db.AuthorRepo, context.Context) {
	tb.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)

	s := NewScanner(downloads, clients, books, authors, history, libraryDir, "", "", "", "")
	return s, books, authors, context.Background()
}

// seedLibrary creates totalBooks untracked .epub files on disk plus totalBooks
// matching "wanted" book rows. The names the scan reads (folder names when
// nested, filename when flat) use a vocabulary disjoint from the book titles,
// so Jaro-Winkler stays below the 0.85 reconcile threshold — the scan does its
// full matching work every iteration with no reconcile side effects, keeping
// iterations identical.
//
// nested=true uses the realistic {Author}/{Book}/file.epub layout with 4 books
// per author, so the scan recovers the author from the folder hierarchy and
// the title tier can scope comparison to that author. nested=false dumps every
// file flat in the library root under one author — the worst case, no author
// scoping possible.
func seedLibrary(tb testing.TB, libDir string, books *db.BookRepo, authors *db.AuthorRepo, totalBooks int, nested bool) {
	tb.Helper()
	ctx := context.Background()
	const booksPerAuthor = 4
	var author *models.Author
	for i := range totalBooks {
		if author == nil || (nested && i%booksPerAuthor == 0) {
			author = &models.Author{
				ForeignID: fmt.Sprintf("OL-A-%06d", i),
				Name:      fmt.Sprintf("Firstname%06d Lastname%06d", i, i),
				SortName:  fmt.Sprintf("Lastname%06d, Firstname%06d", i, i),
			}
			if err := authors.Create(ctx, author); err != nil {
				tb.Fatal(err)
			}
		}
		dbTitle := fmt.Sprintf("Catalogue Record Distinct Entry %06d", i)
		fileBase := fmt.Sprintf("Untracked Manuscript Number %06d.epub", i)
		var fp string
		if nested {
			// The book-folder name is deliberately disjoint from dbTitle: the
			// scan derives the title from this folder (#754), so a matching
			// name here would reconcile the file and skew later iterations.
			dir := filepath.Join(libDir, author.Name, fmt.Sprintf("Unmatched Folder Volume %06d", i))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				tb.Fatal(err)
			}
			fp = filepath.Join(dir, fileBase)
		} else {
			fp = filepath.Join(libDir, fileBase)
		}
		if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
			tb.Fatal(err)
		}
		bk := &models.Book{
			ForeignID: fmt.Sprintf("OL-B-%06d", i),
			AuthorID:  author.ID,
			Title:     dbTitle,
			Status:    models.BookStatusWanted,
		}
		if err := books.Create(ctx, bk); err != nil {
			tb.Fatal(err)
		}
	}
}

// BenchmarkScanLibrary measures a full ScanLibrary pass. "nested" is the
// realistic per-author layout; "flat" is the worst case with no author hints.
func BenchmarkScanLibrary(b *testing.B) {
	for _, layout := range []struct {
		name   string
		nested bool
	}{{"nested", true}, {"flat", false}} {
		for _, n := range []int{500, 2000, 8000} {
			b.Run(fmt.Sprintf("%s-%d", layout.name, n), func(b *testing.B) {
				silenceSlog(b)
				libDir := b.TempDir()
				s, books, authors, ctx := benchScannerFixture(b, libDir)
				seedLibrary(b, libDir, books, authors, n, layout.nested)
				b.ReportAllocs()
				for b.Loop() {
					s.ScanLibrary(ctx)
				}
			})
		}
	}
}

// Package-level sinks defeat dead-code elimination in the micro-benchmarks.
var (
	sinkString string
	sinkFloat  float64
)

// BenchmarkNormalizeTitle isolates the per-call cost of normalizeTitle.
func BenchmarkNormalizeTitle(b *testing.B) {
	titles := []string{
		"The Fragile Threads of Power",
		"Fragile Threads of Power, The",
		"A Darker Shade of Magic",
		"Catalogue Record Distinct Entry 01234",
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		sinkString = normalizeTitle(titles[i%len(titles)])
		i++
	}
}

// BenchmarkJaroWinkler isolates the per-call cost of the fuzzy title-distance
// function the title tier runs per candidate pair.
func BenchmarkJaroWinkler(b *testing.B) {
	left := normalizeTitle("Untracked Manuscript Number 01234")
	right := normalizeTitle("Catalogue Record Distinct Entry 01234")
	b.ReportAllocs()
	for b.Loop() {
		sinkFloat = textutil.JaroWinkler(left, right)
	}
}
