package importer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestRenamerDestPath(t *testing.T) {
	r := NewRenamer("")
	releaseDate := time.Date(2016, 7, 26, 0, 0, 0, 0, time.UTC)

	author := &models.Author{Name: "Test Author"}
	book := &models.Book{
		Title:       "Dark Matter",
		ReleaseDate: &releaseDate,
	}

	got, err := r.DestPath("/books", author, book, "", "", "/downloads/complete/something.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Test Author", "Dark Matter (2016)", "Dark Matter - Test Author.epub")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRenamerNoYear(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Author"}
	book := &models.Book{Title: "Book Title"}

	got, err := r.DestPath("/lib", author, book, "", "", "file.pdf")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/lib", "Author", "Book Title ()", "Book Title - Author.pdf")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRenamerSanitizesPath(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Author: Bad/Name"}
	book := &models.Book{Title: "Title? With <Bad> Chars"}

	got, err := r.DestPath("/lib", author, book, "", "", "test.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	// Verify path doesn't contain dangerous characters in the filename portion
	base := filepath.Base(got)
	for _, bad := range []string{":", "?", "<", ">"} {
		if contains(base, bad) {
			t.Errorf("path %q contains bad char %q", got, bad)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && filepath.Base(s) != "" && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRenamerASINToken(t *testing.T) {
	r := NewRenamer("{Author}/{ASIN} - {Title}.{ext}")
	releaseDate := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	author := &models.Author{Name: "Mary Doria Russell"}
	book := &models.Book{
		Title:       "The Sparrow",
		ASIN:        "B01LVSUORS",
		ReleaseDate: &releaseDate,
	}

	got, err := r.DestPath("/books", author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Mary Doria Russell", "B01LVSUORS - The Sparrow.epub")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRenamerASINTokenEmpty(t *testing.T) {
	// {ASIN} with no ASIN on the book should produce an empty segment
	r := NewRenamer("{ASIN}/{Title}.{ext}")
	author := &models.Author{Name: "Author"}
	book := &models.Book{Title: "Some Book"}

	got, err := r.DestPath("/books", author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Some Book.epub")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRenamerSeriesTokens(t *testing.T) {
	r := NewRenamer("{Author}/{Series}/{SeriesNumber} - {Title}.{ext}")
	releaseDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	author := &models.Author{Name: "Frank Herbert"}
	book := &models.Book{Title: "Dune", ReleaseDate: &releaseDate}

	got, err := r.DestPath("/books", author, book, "Dune Chronicles", "1", "book.m4b")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Frank Herbert", "Dune Chronicles", "1 - Dune.m4b")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRenamerSeriesTokensEmpty(t *testing.T) {
	author := &models.Author{Name: "Author"}
	book := &models.Book{Title: "Standalone"}

	cases := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "series segment dropped",
			template: "{Author}/{Series}/{Title}.{ext}",
			want:     filepath.Join("/books", "Author", "Standalone.epub"),
		},
		{
			// Discord report (Jonathan Stroud "The Hollow Boy"): empty
			// {SeriesNumber} leaves " - " dangling before the title.
			name:     "leading seriesNumber separator stripped",
			template: "{Author}/{Series}/{SeriesNumber} - {Title}.{ext}",
			want:     filepath.Join("/books", "Author", "Standalone.epub"),
		},
		{
			// Both leading series tokens empty: the whole "{Series}" segment
			// drops and the "{SeriesNumber} - " prefix collapses.
			name:     "consecutive leading tokens stripped",
			template: "{Author}/{Series} - {SeriesNumber} - {Title}.{ext}",
			want:     filepath.Join("/books", "Author", "Standalone.epub"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRenamer(tc.template)
			got, err := r.DestPath("/books", author, book, "", "", "book.epub")
			if err != nil {
				t.Fatalf("DestPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRenamerGenreToken(t *testing.T) {
	author := &models.Author{Name: "Frank Herbert"}

	cases := []struct {
		name     string
		template string
		genres   []string
		want     string
	}{
		{
			name:     "genre folder from first genre",
			template: "{Genre}/{Author}/{Title}.{ext}",
			genres:   []string{"Science Fiction", "Fantasy"},
			want:     filepath.Join("/books", "Science Fiction", "Frank Herbert", "Dune.epub"),
		},
		{
			name:     "empty genre drops the segment",
			template: "{Genre}/{Author}/{Title}.{ext}",
			genres:   nil,
			want:     filepath.Join("/books", "Frank Herbert", "Dune.epub"),
		},
		{
			// VENGEANCE's Calibre {#genre:ifempty(Unsorted)} workflow.
			name:     "empty genre uses default",
			template: "{Genre:Unsorted}/{Author}/{Title}.{ext}",
			genres:   nil,
			want:     filepath.Join("/books", "Unsorted", "Frank Herbert", "Dune.epub"),
		},
		{
			name:     "default ignored when genre present",
			template: "{Genre:Unsorted}/{Author}/{Title}.{ext}",
			genres:   []string{"Fantasy"},
			want:     filepath.Join("/books", "Fantasy", "Frank Herbert", "Dune.epub"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRenamer(tc.template)
			book := &models.Book{Title: "Dune", Genres: tc.genres}
			got, err := r.DestPath("/books", author, book, "", "", "book.epub")
			if err != nil {
				t.Fatalf("DestPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRenamerAudiobookSeriesTokens(t *testing.T) {
	r := NewRenamerWithAudiobook("", "{Author}/{Series}/{SeriesNumber} - {Title}")
	author := &models.Author{Name: "Brandon Sanderson"}
	book := &models.Book{Title: "The Way of Kings"}

	got, err := r.AudiobookDestDir("/audiobooks", author, book, "Stormlight Archive", "1")
	if err != nil {
		t.Fatalf("AudiobookDestDir: %v", err)
	}
	want := filepath.Join("/audiobooks", "Brandon Sanderson", "Stormlight Archive", "1 - The Way of Kings")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestMoveFile(t *testing.T) {
	// Create temp source file
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.epub")
	if err := os.WriteFile(srcPath, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	dstPath := filepath.Join(tmpDir, "dest", "subdir", "moved.epub")

	err := MoveFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	// Source should not exist
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Error("source file should be deleted after move")
	}

	// Dest should exist with correct content
	content, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(content) != "test content" {
		t.Errorf("content mismatch: got %q", string(content))
	}
}

func TestUniqueDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "Author", "Title (2020)")

	// Nothing there yet — returned unchanged.
	if got := UniqueDir(target); got != target {
		t.Errorf("free path should return unchanged, got %q want %q", got, target)
	}

	// Occupy the target; next call should append " (2)".
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	want := target + " (2)"
	if got := UniqueDir(target); got != want {
		t.Errorf("first collision: got %q want %q", got, want)
	}

	// Occupy "(2)" too — next call should pick " (3)".
	if err := os.MkdirAll(want, 0755); err != nil {
		t.Fatal(err)
	}
	want = target + " (3)"
	if got := UniqueDir(target); got != want {
		t.Errorf("second collision: got %q want %q", got, want)
	}
}
