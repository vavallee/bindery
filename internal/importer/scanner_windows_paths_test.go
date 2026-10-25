package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_WindowsPathFolding is #3468. Windows keeps an existing folder
// under its own capitals and drops a trailing dot from a folder name, so the
// row Bindery stored after a move ("H.R.H./x.epub", "WASTE LANDS FLIP/x.epub")
// differs from the path a walk reports. The flavour is injected, so this runs
// on Linux CI: the folder on disk is the one Windows would have kept.
func TestScanLibrary_WindowsPathFolding(t *testing.T) {
	cases := []struct {
		name       string
		onDisk     string // folder as the filesystem holds it
		stored     string // folder in the book_files row
		windows    bool
		wantTrack  int
		wantUnmatc int
	}{
		{"trailing dot, windows", "H.R.H", "H.R.H.", true, 1, 0},
		{"trailing dot, posix", "H.R.H", "H.R.H.", false, 0, 1},
		{"other capitals, windows", "Waste Lands Flip", "WASTE LANDS FLIP", true, 1, 0},
		{"other capitals, posix", "Waste Lands Flip", "WASTE LANDS FLIP", false, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, books, authors, settings, libDir, ctx := trackedPathsFixture(t)
			s.windowsPaths = tc.windows
			author := createScanAuthor(t, ctx, authors)

			dir := filepath.Join(libDir, "Stephen King", tc.onDisk)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "Stephen King - Book.epub"), bookSized("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			book := createScanBook(t, ctx, books, author.ID, "ol:hrh", "Book", models.BookStatusImported, models.MediaTypeEbook)
			stored := filepath.Join(libDir, "Stephen King", tc.stored, "Stephen King - Book.epub")
			if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, stored); err != nil {
				t.Fatal(err)
			}

			s.ScanLibrary(ctx)

			p := readScanResult(t, ctx, settings)
			if p.AlreadyTrack != tc.wantTrack || p.Unmatched != tc.wantUnmatc {
				t.Errorf("already_tracked=%d unmatched=%d, want %d and %d", p.AlreadyTrack, p.Unmatched, tc.wantTrack, tc.wantUnmatc)
			}
		})
	}
}

func TestPathKey(t *testing.T) {
	same := [][2]string{
		{`C:\lib\Stephen King\H.R.H.\a.epub`, `c:\lib\stephen king\H.R.H\a.epub`},
		{`C:\lib\Waste Lands Flip\a.epub`, `C:\lib\WASTE LANDS FLIP\a.epub`},
		{`C:\lib\x \a.epub`, `C:\lib\x\a.epub`},
		{`//srv/share/x/a.epub`, `\\srv\share\X\a.epub`},
	}
	for _, c := range same {
		if pathKey(c[0], true) != pathKey(c[1], true) {
			t.Errorf("windows: %q and %q should match: %q vs %q", c[0], c[1], pathKey(c[0], true), pathKey(c[1], true))
		}
	}
	diff := [][2]string{
		{"/lib/H.R.H./a.epub", "/lib/H.R.H/a.epub"},
		{"/lib/Carrie/a.epub", "/lib/CARRIE/a.epub"},
	}
	for _, c := range diff {
		if pathKey(c[0], false) == pathKey(c[1], false) {
			t.Errorf("posix: %q and %q are different files", c[0], c[1])
		}
	}
	if pathKey(`C:\lib\a\b.epub`, true) == pathKey(`C:\lib\a\c.epub`, true) {
		t.Error("windows: different files must stay different")
	}
}

func TestRenamer_WindowsTrailingDots(t *testing.T) {
	book := &models.Book{Title: "H.R.H."}
	author := &models.Author{Name: "Stephen King"}
	tmpl := "{Author}/{Title}/{Author} - {Title}.{ext}"

	win, err := NewRenamer(tmpl).WithWindowsNames(true).DestPath("/lib", author, book, "", "", "x.epub")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/lib", "Stephen King", "H.R.H", "Stephen King - H.R.H..epub"); win != want {
		t.Errorf("windows dest = %q, want %q", win, want)
	}

	posix, err := NewRenamer(tmpl).WithWindowsNames(false).DestPath("/lib", author, book, "", "", "x.epub")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/lib", "Stephen King", "H.R.H.", "Stephen King - H.R.H..epub"); posix != want {
		t.Errorf("posix dest = %q, want %q (Linux paths must not change)", posix, want)
	}

	jr := &models.Author{Name: "Martin Luther King Jr."}
	got, _ := NewRenamer("{Author}/{Title}.{ext}").WithWindowsNames(true).DestPath("/lib", jr, &models.Book{Title: "T"}, "", "", "x.epub")
	if want := filepath.Join("/lib", "Martin Luther King Jr", "T.epub"); got != want {
		t.Errorf("author ending in a dot: %q, want %q", got, want)
	}
}

// TestReorganize_WindowsFoldedPathIsNoop covers the preview half of #3468: a
// row stored under other capitals than the template renders is the same file on
// Windows, so the preview says noop instead of proposing a move or a collision.
func TestReorganize_WindowsFoldedPathIsNoop(t *testing.T) {
	for _, windows := range []bool{true, false} {
		env, libraryDir, _, ctx := reorgFixture(t)
		env.s.windowsPaths = windows
		book := env.seed(t, ctx, "Jane Doe", "My Book")
		p := filepath.Join(libraryDir, "Jane Doe", "MY BOOK (2020)", "My Book - Jane Doe.epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
		if err != nil || len(moves) != 1 {
			t.Fatalf("preview: %v %+v", err, moves)
		}
		if got := moves[0].Status == ReorgStatusNoop; got != windows {
			t.Errorf("windows=%v: status %q, noop=%v", windows, moves[0].Status, got)
		}
	}
}

func TestRenamer_WindowsDotOnlySegmentKeepsPlaceholder(t *testing.T) {
	book := &models.Book{Title: "..."}
	author := &models.Author{Name: "Jane Doe"}
	tmpl := "{Author}/{Title}/{Author} - {Title}.{ext}"
	got, err := NewRenamer(tmpl).WithWindowsNames(true).DestPath("/lib", author, book, "", "", "x.epub")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/lib", "Jane Doe", "_", "Jane Doe - ....epub"); got != want {
		t.Errorf("dest = %q, want %q", got, want)
	}
	posix, _ := NewRenamer(tmpl).WithWindowsNames(false).DestPath("/lib", author, book, "", "", "x.epub")
	if want := filepath.Join("/lib", "Jane Doe", "...", "Jane Doe - ....epub"); posix != want {
		t.Errorf("posix dest = %q, want %q", posix, want)
	}
}

// TestReorganize_WindowsAudiobookFolderFoldedIsNoop: an audiobook folder stored
// under legacy capitals or a trailing dot is the rendered folder on Windows, so
// the preview is a noop and never proposes the " (2)" variant (#3468).
func TestReorganize_WindowsAudiobookFolderFoldedIsNoop(t *testing.T) {
	for _, tc := range []struct{ title, stored string }{
		{"My Book", "MY BOOK (2020)"},
	} {
		for _, windows := range []bool{true, false} {
			env, _, audioDir, ctx := reorgFixture(t)
			env.s.windowsPaths = windows
			book := env.seed(t, ctx, "Jane Doe", tc.title)
			rendered := filepath.Join(audioDir, "Jane Doe", "My Book (2020)")
			stored := filepath.Join(audioDir, "Jane Doe", tc.stored)
			for _, d := range []string{rendered, stored} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeFileAt(t, filepath.Join(stored, "part1.mp3"))
			if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, stored); err != nil {
				t.Fatal(err)
			}
			moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
			if err != nil || len(moves) != 1 {
				t.Fatalf("preview: %v %+v", err, moves)
			}
			if got := moves[0].Status == ReorgStatusNoop; got != windows {
				t.Errorf("windows=%v: status %q proposed %q", windows, moves[0].Status, moves[0].Proposed)
			}
		}
	}
}

func TestAlreadyImportedPath_WindowsFold(t *testing.T) {
	for _, windows := range []bool{true, false} {
		env, libDir, _, ctx := reorgFixture(t)
		env.s.windowsPaths = windows
		book := env.seed(t, ctx, "Jane Doe", "H.R.H.")
		dir := filepath.Join(libDir, "Jane Doe", "H.R.H")
		stored := filepath.Join(libDir, "Jane Doe", "H.R.H.", "a.epub")
		writeFileAt(t, filepath.Join(dir, "a.epub"))
		if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, stored); err != nil {
			t.Fatal(err)
		}
		// Linux cannot stat the stored spelling, so only the path compare is
		// asserted by creating it as well.
		writeFileAt(t, stored)
		got := env.s.alreadyImportedPath(ctx, book, filepath.Join(dir, "a.epub"))
		if got != windows {
			t.Errorf("windows=%v: alreadyImportedPath=%v", windows, got)
		}
	}
}
