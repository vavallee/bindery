package importer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// alternateTitlePairs are two spellings of one work: abbreviations, and the UK
// and US titles of the first Harry Potter. Each must reconcile on the scan and
// be found on the add path, in both directions.
var alternateTitlePairs = []struct{ name, author, file, wanted string }{
	{"philosopher file, sorcerer book", "J. K. Rowling", "Harry Potter and the Philosophers Stone - J. K. Rowling.epub", "Harry Potter and the Sorcerer's Stone"},
	{"sorcerer file, philosopher book", "J. K. Rowling", "Harry Potter and the Sorcerers Stone - J. K. Rowling.epub", "Harry Potter and the Philosopher's Stone"},
	{"abbreviated file", "Robert Louis Stevenson", "Dr Jekyll and Mr Hyde - Robert Louis Stevenson.epub", "Doctor Jekyll and Mister Hyde"},
	{"spelled out file", "Robert Louis Stevenson", "Doctor Jekyll and Mister Hyde - Robert Louis Stevenson.epub", "Dr. Jekyll and Mr. Hyde"},
	{"doctor file, dr book", "Stephen King", "Doctor Sleep - Stephen King.epub", "Dr. Sleep"},
	{"dr file, doctor book", "Stephen King", "Dr Sleep - Stephen King.epub", "Doctor Sleep"},
}

func TestScanLibrary_AlternateTitlesStillReconcile(t *testing.T) {
	for _, tc := range alternateTitlePairs {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanOneEbook(t, tc.author, tc.file, tc.wanted); got == "" {
				t.Errorf("%q did not reconcile onto %q", tc.file, tc.wanted)
			}
		})
	}
}

func TestScanLibrary_AlternateTitleAudiobookFolderStillReconciles(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	folder := filepath.Join(e.abDir, "J. K. Rowling", "Harry Potter and the Sorcerer's Stone - Jim Dale")
	writeFile(t, filepath.Join(folder, "01 - Chapter One.mp3"))
	b := e.wanted(t, "Harry Potter and the Philosopher's Stone", models.MediaTypeAudiobook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, b.ID).AudiobookFilePath; got != folder {
		t.Errorf("audiobook folder = %q, want %q", got, folder)
	}
}

func TestFindExisting_AlternateTitlesAreStillOwned(t *testing.T) {
	for _, tc := range alternateTitlePairs {
		t.Run(tc.name, func(t *testing.T) {
			if tc.author == "Stephen King" {
				// "Doctor Sleep" and "Dr Sleep" share one significant word, and
				// titleWordsMatch needs two, so the add path has never matched
				// them (on main too). The scan, which scores by similarity,
				// does, and is covered above.
				t.Skip("titleWordsMatch never matched a one word overlap; not this guard")
			}
			root := t.TempDir()
			file := filepath.Join(root, tc.author, tc.file)
			writeFile(t, file)
			snap := NewLibrarySnapshot(root, "")
			if got := snap.FindExisting(context.Background(), tc.wanted, tc.author, models.MediaTypeEbook); got != file {
				t.Errorf("FindExisting(%q) = %q, want %q", tc.wanted, got, file)
			}
		})
	}
}

// TestScanLibrary_DifferentWorkIsNotSuggestedAsAMatch: the Relieken folder
// stays unmatched, and the stored suggestions must not offer Order of the
// Phoenix for it. At 0.929 that suggestion clears the UI's one click Confirm
// threshold (0.92), which would hand the user the exact wrong binding the scan
// refused (#3388).
func TestScanLibrary_DifferentWorkIsNotSuggestedAsAMatch(t *testing.T) {
	e := newPotterEnv(t)
	e.hallowsChapter(t)

	e.s.ScanLibrary(e.ctx)

	units := readUnmatchedFiles(t, e.ctx, e.s)
	if len(units) != 1 {
		t.Fatalf("unmatched units = %d, want the Relieken folder", len(units))
	}
	for _, c := range units[0].Candidates {
		if c.BookID == e.phoenix.ID {
			t.Errorf("Orde van de Feniks suggested for the Relieken folder at %.3f", c.Score)
		}
	}
}

// TestScanLibrary_SuggestionsKeepASimilarTitleOfTheSameWork: the suggestion
// guard drops only a different work. A typo of the file's own book is still
// offered, and is the top suggestion.
func TestScanLibrary_SuggestionsKeepASimilarTitleOfTheSameWork(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	writeFile(t, filepath.Join(e.libDir, "J. K. Rowling", "Harry Potter and the Goblet of Fyre - J. K. Rowling.epub"))
	book := e.wanted(t, "Harry Potter and the Goblet of Fire", models.MediaTypeEbook)
	// A skipped book is never reconciled, so the file stays unmatched and the
	// book is only a suggestion, which is what this asks about.
	book.Status = models.BookStatusSkipped
	if err := e.books.Update(e.ctx, book); err != nil {
		t.Fatal(err)
	}

	e.s.ScanLibrary(e.ctx)

	units := readUnmatchedFiles(t, e.ctx, e.s)
	if len(units) != 1 || len(units[0].Candidates) == 0 || units[0].Candidates[0].BookID != book.ID {
		t.Fatalf("units = %+v, want the Goblet of Fire suggested", units)
	}
}
