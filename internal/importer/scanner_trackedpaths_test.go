package importer

// Regression tests for #1436: ScanLibrary tracked one registered file's parent
// directory for every format, so in a flat Author/Title.epub layout one
// tracked book hid every untracked sibling ebook in the same author folder —
// silently, with the skips counted nowhere ("6 found, 0 reconciled, 2
// unmatched"). Parent-dir suppression is now scoped to audiobook folders and
// every walked file lands in exactly one counter.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func trackedPathsFixture(t *testing.T) (s *Scanner, books *db.BookRepo, authors *db.AuthorRepo, settings *db.SettingsRepo, libDir string, ctx context.Context) {
	t.Helper()
	libDir = t.TempDir()
	s, books, authors, settings, ctx = visibilityFixture(t, libDir, "")
	return
}

func createScanAuthor(t *testing.T, ctx context.Context, authors *db.AuthorRepo) *models.Author {
	t.Helper()
	author := &models.Author{ForeignID: "ol:doe", Name: "Jane Doe", SortName: "Doe, Jane", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return author
}

func createScanBook(t *testing.T, ctx context.Context, books *db.BookRepo, authorID int64, foreignID, title, status, mediaType string) *models.Book {
	t.Helper()
	book := &models.Book{
		ForeignID: foreignID, AuthorID: authorID, Title: title, SortTitle: title,
		Status: status, Monitored: true, AnyEditionOK: true,
		MediaType: mediaType, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	return book
}

// TestScanLibrary_TrackedSiblingEbooksStillScanned is the flat-layout repro:
// three epubs in one author folder, one of them already registered. The two
// untracked siblings must be reconciled, not silently skipped because their
// parent directory carries a tracked file, and the counters must add up.
func TestScanLibrary_TrackedSiblingEbooksStillScanned(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	trackedPath := write("Alpha Adventure.epub")
	write("Beta Business.epub")
	write("Gamma Games.epub")

	tracked := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeEbook)
	if err := books.AddBookFile(ctx, tracked.ID, models.MediaTypeEbook, trackedPath); err != nil {
		t.Fatal(err)
	}
	createScanBook(t, ctx, books, author.ID, "ol:beta", "Beta Business", models.BookStatusWanted, models.MediaTypeEbook)
	createScanBook(t, ctx, books, author.ID, "ol:gamma", "Gamma Games", models.BookStatusWanted, models.MediaTypeEbook)

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.FilesFound != 3 {
		t.Fatalf("FilesFound = %d, want 3", p.FilesFound)
	}
	if p.Reconciled != 2 {
		t.Errorf("Reconciled = %d, want 2 (siblings of a tracked ebook must still be scanned)", p.Reconciled)
	}
	if p.AlreadyTrack != 1 {
		t.Errorf("AlreadyTrack = %d, want 1 (the registered file itself)", p.AlreadyTrack)
	}
	if got := p.Reconciled + p.Unmatched + p.AlreadyTrack; got != p.FilesFound {
		t.Errorf("counters don't add up: reconciled+unmatched+already_tracked = %d, files_found = %d", got, p.FilesFound)
	}
}

// TestScanLibrary_AudiobookFolderSiblingsCountAsTracked pins the behaviour the
// directory tracking was built for: sibling tracks inside an already-imported
// audiobook folder are still suppressed, but now show up in already_tracked
// instead of vanishing from the totals.
func TestScanLibrary_AudiobookFolderSiblingsCountAsTracked(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe", "Delta Drama")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	track1 := filepath.Join(dir, "01 - track.mp3")
	track2 := filepath.Join(dir, "02 - track.mp3")
	for _, p := range []string{track1, track2} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	book := createScanBook(t, ctx, books, author.ID, "ol:delta", "Delta Drama", models.BookStatusImported, models.MediaTypeAudiobook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, track1); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.FilesFound != 2 {
		t.Fatalf("FilesFound = %d, want 2", p.FilesFound)
	}
	if p.Unmatched != 0 {
		t.Errorf("Unmatched = %d, want 0 (sibling track belongs to the tracked audiobook)", p.Unmatched)
	}
	if p.AlreadyTrack != 2 {
		t.Errorf("AlreadyTrack = %d, want 2 (registered track + sibling in its folder)", p.AlreadyTrack)
	}
}
