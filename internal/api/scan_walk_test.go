package api

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// unitNames returns the sorted basenames of the enumerated units.
func unitNames(units []scanUnit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.name
	}
	sort.Strings(out)
	return out
}

// unitByName indexes units by basename for isDir assertions.
func unitByName(units []scanUnit) map[string]scanUnit {
	m := make(map[string]scanUnit, len(units))
	for _, u := range units {
		m[u.name] = u
	}
	return m
}

// TestEnumerateImportUnits_BoundaryHeuristic table-drives the recursive walk and
// the unit-boundary decision at the heart of #1434: which folders are one book
// and which are shelves of many.
func TestEnumerateImportUnits_BoundaryHeuristic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		files     []string        // book files (and non-book noise) to create under root
		wantNames []string        // expected unit basenames (sorted)
		wantDirs  map[string]bool // basename -> isDir, checked for the listed names
	}{
		{
			name:      "flat author/title yields the file, not the author folder",
			files:     []string{"Andy Weir/Project Hail Mary.epub"},
			wantNames: []string{"Project Hail Mary.epub"},
			wantDirs:  map[string]bool{"Project Hail Mary.epub": false},
		},
		{
			name:      "nested author/books/title is not collapsed to one row",
			files:     []string{"Andy Weir/Books/Project Hail Mary.epub"},
			wantNames: []string{"Project Hail Mary.epub"},
			wantDirs:  map[string]bool{"Project Hail Mary.epub": false},
		},
		{
			name: "multi-format folder is ONE unit (same book, two formats)",
			files: []string{
				"Andy Weir/Project Hail Mary/Project Hail Mary.epub",
				"Andy Weir/Project Hail Mary/Project Hail Mary.mobi",
			},
			wantNames: []string{"Project Hail Mary"},
			wantDirs:  map[string]bool{"Project Hail Mary": true},
		},
		{
			name: "different-stem ebooks in a leaf folder are separate books",
			files: []string{
				"Andy Weir/Collection/The Martian.epub",
				"Andy Weir/Collection/Artemis.epub",
			},
			wantNames: []string{"Artemis.epub", "The Martian.epub"},
		},
		{
			name: "multi-disc audiobook folder is ONE unit",
			files: []string{
				"Frank Herbert/Dune/CD1/01.mp3",
				"Frank Herbert/Dune/CD1/02.mp3",
				"Frank Herbert/Dune/CD2/01.mp3",
			},
			wantNames: []string{"Dune"},
			wantDirs:  map[string]bool{"Dune": true},
		},
		{
			name:      "loose audio folder is ONE audiobook unit",
			files:     []string{"Frank Herbert/Dune/dune.m4b"},
			wantNames: []string{"Dune"},
			wantDirs:  map[string]bool{"Dune": true},
		},
		{
			name: "author folder of single-file ebooks is MANY units",
			files: []string{
				"Brandon Sanderson/Mistborn.epub",
				"Brandon Sanderson/Elantris.epub",
				"Brandon Sanderson/Warbreaker.epub",
			},
			wantNames: []string{"Elantris.epub", "Mistborn.epub", "Warbreaker.epub"},
		},
		{
			name: "author folder of audiobook folders is MANY units",
			files: []string{
				"Some Author/Book A/a.mp3",
				"Some Author/Book B/b.mp3",
			},
			wantNames: []string{"Book A", "Book B"},
			wantDirs:  map[string]bool{"Book A": true, "Book B": true},
		},
		{
			name: "non-book files are ignored",
			files: []string{
				"Andy Weir/Project Hail Mary.epub",
				"Andy Weir/cover.jpg",
				"Andy Weir/desktop.ini",
			},
			wantNames: []string{"Project Hail Mary.epub"},
		},
		{
			name: "mixed loose ebook and nested book coexist",
			files: []string{
				"loose.epub",
				"Author/Sub/deep.epub",
			},
			wantNames: []string{"deep.epub", "loose.epub"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, f := range tc.files {
				writeTestFile(t, filepath.Join(root, filepath.FromSlash(f)))
			}

			units, truncated := enumerateImportUnits(root, 1000)
			if truncated {
				t.Errorf("unexpected truncation for a small tree")
			}
			got := unitNames(units)
			if len(got) != len(tc.wantNames) {
				t.Fatalf("units = %v, want %v", got, tc.wantNames)
			}
			for i := range got {
				if got[i] != tc.wantNames[i] {
					t.Fatalf("units = %v, want %v", got, tc.wantNames)
				}
			}
			byName := unitByName(units)
			for name, wantDir := range tc.wantDirs {
				u, ok := byName[name]
				if !ok {
					t.Errorf("expected a unit named %q", name)
					continue
				}
				if u.isDir != wantDir {
					t.Errorf("unit %q isDir = %v, want %v", name, u.isDir, wantDir)
				}
			}
		})
	}
}

// TestEnumerateImportUnits_Truncates verifies the unit cap trips truncated and
// stops collecting once the limit is reached.
func TestEnumerateImportUnits_Truncates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		writeTestFile(t, filepath.Join(root, "Author", string(rune('a'+i))+".epub"))
	}
	units, truncated := enumerateImportUnits(root, 4)
	if !truncated {
		t.Errorf("expected truncated=true when units exceed the limit")
	}
	if len(units) != 4 {
		t.Errorf("units = %d, want exactly the limit of 4", len(units))
	}
}

// TestEnumerateImportUnits_UnreadableDirSkipped confirms an unreadable
// subdirectory is skipped rather than aborting the whole walk.
func TestEnumerateImportUnits_UnreadableDirSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory mode bits do not restrict access")
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Readable", "book.epub"))
	locked := filepath.Join(root, "Locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(locked, "hidden.epub"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	units, _ := enumerateImportUnits(root, 1000)
	// The readable book is still enumerated; the locked dir is silently skipped.
	got := unitNames(units)
	if len(got) != 1 || got[0] != "book.epub" {
		t.Errorf("units = %v, want just [book.epub] (locked dir skipped)", got)
	}
}
