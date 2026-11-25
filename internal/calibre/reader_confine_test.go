package calibre

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A metadata.db whose books.path or data.name climbs out of the library must
// not yield paths outside it.
func TestReader_BooksConfinedToLibraryRoot(t *testing.T) {
	root := buildFixtureLibrary(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.epub"), []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "cover.jpg"), []byte("cover"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}

	d, err := sql.Open("sqlite", filepath.Join(root, metadataDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO books (id, title, sort, path) VALUES (10, 'Escape', 'Escape', '` + filepath.ToSlash(rel) + `')`,
		`INSERT INTO data (book, format, uncompressed_size, name) VALUES (10, 'EPUB', 1, 'x')`,
		`INSERT INTO books (id, title, sort, path) VALUES (11, 'NameEscape', 'NameEscape', 'Alice Author/Book One (1)')`,
		`INSERT INTO data (book, format, uncompressed_size, name) VALUES (11, 'EPUB', 1, '../Book Two (2)/booktwo')`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	d.Close()

	r, err := OpenReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	seen := 0
	err = r.Books(context.Background(), func(cb CalibreBook) error {
		switch cb.CalibreID {
		case 10:
			seen++
			if cb.LibraryPath != "" || cb.CoverPath != "" {
				t.Errorf("escaping books.path kept LibraryPath=%q CoverPath=%q", cb.LibraryPath, cb.CoverPath)
			}
			for _, f := range cb.Formats {
				if f.AbsolutePath != "" {
					t.Errorf("escaping books.path produced format path %q", f.AbsolutePath)
				}
			}
		case 11:
			seen++
			for _, f := range cb.Formats {
				if f.AbsolutePath != "" {
					t.Errorf("data.name with .. produced format path %q", f.AbsolutePath)
				}
			}
		default:
			for _, f := range cb.Formats {
				if !strings.HasPrefix(f.AbsolutePath, root) {
					t.Errorf("book %d format path %q outside the library", cb.CalibreID, f.AbsolutePath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("saw %d of the 2 crafted books", seen)
	}
}

func TestCalibreEnv_DropsBinderyVariables(t *testing.T) {
	got := calibreEnv([]string{"PATH=/usr/bin", "BINDERY_API_KEY=secret", "bindery_x=y", "HOME=/h", "NOT_BINDERY_X=1"})
	want := []string{"PATH=/usr/bin", "HOME=/h", "NOT_BINDERY_X=1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("calibreEnv = %v, want %v", got, want)
	}
}
