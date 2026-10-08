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

// TestScanLibrary_FlatLayoutTrackShapesStayTracked covers track names that
// carry chapter titles or spelled part numbers. Main absorbed all of these
// with the tracked first track; the #1985 narrowing must not turn them into
// unmatched rows. Only the text before the first track number is compared.
func TestScanLibrary_FlatLayoutTrackShapesStayTracked(t *testing.T) {
	cases := []struct {
		name  string
		files []string // the first one is tracked
	}{
		{"chapter titles after a leading number", []string{"01 - Chapter One.mp3", "02 - Chapter Two.mp3", "03 - The Reckoning.mp3"}},
		{"title, number, chapter title", []string{"Alpha Adventure - 01 - The Beginning.mp3", "Alpha Adventure - 02 - The Middle.mp3"}},
		{"spelled part numbers", []string{"Alpha Adventure Part One.mp3", "Part Two.mp3", "Alpha Adventure Part Three.mp3"}},
		{"n of m", []string{"Alpha Adventure.mp3", "Alpha Adventure (1 of 12).mp3", "Alpha Adventure (2 of 12).mp3"}},
		{"cd glued to its number", []string{"Alpha Adventure CD1.mp3", "Alpha Adventure CD2.mp3", "Alpha Adventure.mp3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
			author := createScanAuthor(t, ctx, authors)
			dir := filepath.Join(libDir, "Jane Doe")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, n := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, n), bookSized("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			book := createScanBook(t, ctx, books, author.ID, "ol:alpha", "Alpha Adventure", models.BookStatusImported, models.MediaTypeAudiobook)
			if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, filepath.Join(dir, tc.files[0])); err != nil {
				t.Fatal(err)
			}

			s.ScanLibrary(ctx)

			p := readScanResult(t, ctx, settings)
			if p.AlreadyTrack != len(tc.files) || p.Unmatched != 0 {
				t.Errorf("already_tracked=%d unmatched=%d, want %d and 0", p.AlreadyTrack, p.Unmatched, len(tc.files))
			}
		})
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

func TestAudioTrackStem(t *testing.T) {
	cases := map[string]string{
		"Alpha Adventure.mp3":                   "alpha adventure",
		"Alpha Adventure - Part 01.mp3":         "alpha adventure",
		"Alpha Adventure CD2.mp3":               "alpha adventure",
		"Alpha Adventure Part Two.mp3":          "alpha adventure",
		"Alpha Adventure (2 of 12).mp3":         "alpha adventure",
		"Alpha Adventure - 02 - The Middle.mp3": "alpha adventure",
		"02 - Chapter Two.mp3":                  "",
		"Part Two.mp3":                          "",
		"04.mp3":                                "",
		"Beta Business.mp3":                     "beta business",
		"The Chapter House.mp3":                 "the house",
		"Ready Player One.mp3":                  "ready player one",
	}
	for name, want := range cases {
		if got := audioTrackStem(name); got != want {
			t.Errorf("audioTrackStem(%q) = %q, want %q", name, got, want)
		}
	}
}
