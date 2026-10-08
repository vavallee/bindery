package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_FlatLayoutUntrackedAudiobookReconciles is the reproduction
// for #1985. In a flat Author/Title.mp3 layout a tracked audiobook's parent is
// the author folder, and marking it as tracked absorbed every other audiobook
// by that author as already tracked, so a wanted one never reconciled and
// never showed up as unmatched either.
func TestScanLibrary_FlatLayoutUntrackedAudiobookReconciles(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	trackedPath := filepath.Join(dir, "Alpha Adventure.mp3")
	untrackedPath := filepath.Join(dir, "Beta Business.mp3")
	for _, p := range []string{trackedPath, untrackedPath} {
		if err := os.WriteFile(p, bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tracked := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeAudiobook)
	if err := books.AddBookFile(ctx, tracked.ID, models.MediaTypeAudiobook, trackedPath); err != nil {
		t.Fatal(err)
	}
	wanted := createScanBook(t, ctx, books, author.ID, "ol:beta", "Beta Business", models.BookStatusWanted, models.MediaTypeAudiobook)

	s.ScanLibrary(ctx)

	got := bookFileFormats(t, books, ctx, wanted.ID)
	if got[models.MediaTypeAudiobook] != filepath.Clean(untrackedPath) {
		t.Fatalf("Beta Business.mp3 was not reconciled to its book: files = %v", got)
	}
	p := readScanResult(t, ctx, settings)
	if p.Reconciled != 1 || p.AlreadyTrack != 1 {
		t.Errorf("reconciled=%d already_tracked=%d, want 1 and 1", p.Reconciled, p.AlreadyTrack)
	}
}

// TestScanLibrary_FlatLayoutSiblingTracksStayTracked keeps what the parent
// marking was for (#1436) in the flat layout too: the other tracks of a
// tracked multi-file audiobook, named after the same book or by track number
// alone, are counted as already tracked rather than sent to the matchers.
func TestScanLibrary_FlatLayoutSiblingTracksStayTracked(t *testing.T) {
	s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
	author := createScanAuthor(t, ctx, authors)

	dir := filepath.Join(libDir, "Jane Doe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"Alpha Adventure - Part 01.mp3", "Alpha Adventure - Part 02.mp3", "Alpha Adventure 03.mp3", "04.mp3"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), bookSized("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeAudiobook)
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, filepath.Join(dir, names[0])); err != nil {
		t.Fatal(err)
	}

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.AlreadyTrack != len(names) || p.Unmatched != 0 {
		t.Errorf("already_tracked=%d unmatched=%d, want %d and 0: sibling tracks of a tracked audiobook must stay tracked",
			p.AlreadyTrack, p.Unmatched, len(names))
	}
}
