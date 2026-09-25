package api

import (
	"path/filepath"
	"testing"
)

// TestEnumerateImportUnits_SeriesVersusDiscSet pins both directions of the
// unit boundary that #2672 got wrong, because being wrong either way loses a
// book.
//
// Merging too eagerly (the bug) offered a series laid out as "Mistborn/Book 1",
// "Mistborn/Book 2" as ONE row, so importing it attached three books' audio to
// one book and the other two never got their files.
//
// Splitting too eagerly costs the same: a genuine multi disc recording offered
// as one row per disc sends each disc to a different book, and the second
// audiobook for a book that already has one is dropped by the import's
// idempotency guard. So the disc cases below are as load bearing as the series
// ones.
func TestEnumerateImportUnits_SeriesVersusDiscSet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		// Separate books that used to be merged (#2672).
		{
			name: "Book N folders under a series are separate books",
			files: []string{
				"Mistborn/Book 1/01.mp3",
				"Mistborn/Book 2/01.mp3",
				"Mistborn/Book 3/01.mp3",
			},
			want: []string{"Book 1", "Book 2", "Book 3"},
		},
		{
			name: "Vol N folders under a series are separate books",
			files: []string{
				"Wheel of Time/Vol 1/01.mp3",
				"Wheel of Time/Vol. 2/01.mp3",
				"Wheel of Time/Volume 3/01.mp3",
			},
			want: []string{"Vol 1", "Vol. 2", "Volume 3"},
		},
		{
			name: "bare numbered folders under an author are separate books",
			files: []string{
				"Some Author/1/01.mp3",
				"Some Author/2/01.mp3",
			},
			want: []string{"1", "2"},
		},
		{
			name: "mixed Book N and a named folder are still separate books",
			files: []string{
				"Mistborn/Book 1/01.mp3",
				"Mistborn/The Well of Ascension/01.mp3",
			},
			want: []string{"Book 1", "The Well of Ascension"},
		},

		// One recording split across folders, which must stay ONE unit.
		{
			name: "CD folders are one multi disc audiobook",
			files: []string{
				"Frank Herbert/Dune/CD1/01.mp3",
				"Frank Herbert/Dune/CD2/01.mp3",
			},
			want: []string{"Dune"},
		},
		{
			name: "Disc and Disk folders are one multi disc audiobook",
			files: []string{
				"Frank Herbert/Dune/Disc 1/01.mp3",
				"Frank Herbert/Dune/Disk 2/01.mp3",
			},
			want: []string{"Dune"},
		},
		{
			name: "Part folders are one split recording",
			files: []string{
				"Frank Herbert/Dune/Part 1/01.mp3",
				"Frank Herbert/Dune/Part 2/01.mp3",
			},
			want: []string{"Dune"},
		},
		{
			name: "Chapter folders are one split recording",
			files: []string{
				"Frank Herbert/Dune/Chapter 01/01.mp3",
				"Frank Herbert/Dune/Chapter 02/01.mp3",
			},
			want: []string{"Dune"},
		},
		{
			name: "a lone CD1 folder is still that book",
			files: []string{
				"Frank Herbert/Dune/CD1/01.mp3",
			},
			want: []string{"Dune"},
		},
		{
			name: "a disc folder with no audio beneath it never merges",
			files: []string{
				"Frank Herbert/Dune/CD1/01.mp3",
				"Frank Herbert/Dune/CD2/cover.jpg",
			},
			want: []string{"CD1"},
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
				t.Fatalf("unexpected truncation for a small tree")
			}
			got := unitNames(units)
			if len(got) != len(tc.want) {
				t.Fatalf("units = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("units = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
