package importer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_ExactTitleBeatsShorterPartial is #2941 as reported. Library
// order visits "Harry Potter" first, and its score against the file is about
// 0.869, so the first hit over 0.85 took the epub and left the exact book
// with no file. The exact title is the one that owns the path.
func TestScanLibrary_ExactTitleBeatsShorterPartial(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	epub := filepath.Join(e.libDir, "J. K. Rowling", "Harry Potter en het vervloekte kind (2016)",
		"Harry Potter en het vervloekte kind - J. K. Rowling.epub")
	writeFile(t, epub)
	exact := e.wanted(t, "Harry Potter en het vervloekte kind", models.MediaTypeEbook)
	short := e.wanted(t, "Harry Potter", models.MediaTypeBoth)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, exact.ID); got.EbookFilePath != epub {
		t.Errorf("exact book EbookFilePath = %q, want %q", got.EbookFilePath, epub)
	}
	if got := e.get(t, short.ID); got.EbookFilePath != "" {
		t.Errorf("shorter book took %q", got.EbookFilePath)
	}
}

// TestScanLibrary_ExactTitleWinsInsideTheMargin: the near spelling scores
// about 0.990 and sorts first. A margin check alone would leave both
// unmatched; an exact normalised title still wins.
func TestScanLibrary_ExactTitleWinsInsideTheMargin(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	epub := filepath.Join(e.libDir, "J. K. Rowling", "Harry Potter and the Philosophers Stone - J. K. Rowling.epub")
	writeFile(t, epub)
	exact := e.wanted(t, "Harry Potter and the Philosophers Stone", models.MediaTypeEbook)
	near := e.wanted(t, "Harry Potter and the Philosopher s Stone", models.MediaTypeEbook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, exact.ID); got.EbookFilePath != epub {
		t.Errorf("exact book EbookFilePath = %q, want %q", got.EbookFilePath, epub)
	}
	if got := e.get(t, near.ID); got.EbookFilePath != "" {
		t.Errorf("near spelling took %q", got.EbookFilePath)
	}
}

// TestScanLibrary_CloseTitlesStayUnmatched: "Project Hail Mary" (about 0.926,
// sorts first) and "Project Hail Mary: A Novel" (about 0.939) against
// "Project Hail Mary Annotated". The gap is about 0.013, so neither book is
// claimed and the file is left for the user, best score first.
func TestScanLibrary_CloseTitlesStayUnmatched(t *testing.T) {
	e := newVolumeScanEnv(t, "Andy Weir")
	writeFile(t, filepath.Join(e.libDir, "Andy Weir", "Project Hail Mary Annotated - Andy Weir.epub"))
	novel := e.wanted(t, "Project Hail Mary: A Novel", models.MediaTypeEbook)
	plain := e.wanted(t, "Project Hail Mary", models.MediaTypeEbook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, plain.ID); got.EbookFilePath != "" {
		t.Errorf("Project Hail Mary took %q", got.EbookFilePath)
	}
	if got := e.get(t, novel.ID); got.EbookFilePath != "" {
		t.Errorf("Project Hail Mary: A Novel took %q", got.EbookFilePath)
	}
	units := readUnmatchedFiles(t, e.ctx, e.s)
	if len(units) != 1 {
		t.Fatalf("unmatched units = %d, want 1", len(units))
	}
	c := units[0].Candidates
	if len(c) < 2 || c[0].BookID != novel.ID || c[1].BookID != plain.ID || c[0].Score < c[1].Score {
		t.Errorf("candidates = %+v, want the novel then Project Hail Mary", c)
	}
}

// TestScanLibrary_DecisiveGapClaimsTheCloserTitle: "Harry Potter" still clears
// 0.85 (about 0.857) and sorts first, but the longer title leads by about
// 0.11, so the file goes to the longer book.
func TestScanLibrary_DecisiveGapClaimsTheCloserTitle(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	epub := filepath.Join(e.libDir, "J. K. Rowling", "Harry Potter en het vervloekte kind script - J. K. Rowling.epub")
	writeFile(t, epub)
	longer := e.wanted(t, "Harry Potter en het vervloekte kind", models.MediaTypeEbook)
	short := e.wanted(t, "Harry Potter", models.MediaTypeEbook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, longer.ID); got.EbookFilePath != epub {
		t.Errorf("longer book EbookFilePath = %q, want %q", got.EbookFilePath, epub)
	}
	if got := e.get(t, short.ID); got.EbookFilePath != "" {
		t.Errorf("shorter book took %q", got.EbookFilePath)
	}
}

// TestScanLibrary_IdenticalNormalisedTitlesStayUnmatched: two wanted books
// fold to the same title and the file carries it, so both score 1. A tie at
// 1.0 claims neither book.
func TestScanLibrary_IdenticalNormalisedTitlesStayUnmatched(t *testing.T) {
	e := newVolumeScanEnv(t, "Jane Doe")
	writeFile(t, filepath.Join(e.libDir, "Jane Doe", "The Fragile Threads of Power - Jane Doe.epub"))
	leading := e.wanted(t, "The Fragile Threads of Power", models.MediaTypeEbook)
	inverted := e.wanted(t, "Fragile Threads of Power, The", models.MediaTypeEbook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, leading.ID); got.EbookFilePath != "" {
		t.Errorf("leading article took %q", got.EbookFilePath)
	}
	if got := e.get(t, inverted.ID); got.EbookFilePath != "" {
		t.Errorf("inverted article took %q", got.EbookFilePath)
	}
	if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 1 {
		t.Fatalf("unmatched units = %d, want 1", len(units))
	}
}

func TestScanLibrary_BlockedTitleStaysInRanking(t *testing.T) {
	for _, scenario := range []string{"same scan", "previous scan", "script first", "near blocked", "dual format"} {
		t.Run(scenario, func(t *testing.T) {
			e := newVolumeScanEnv(t, "J. K. Rowling")
			title := "Harry Potter en het vervloekte kind"
			mediaType := models.MediaTypeEbook
			if scenario == "dual format" {
				mediaType = models.MediaTypeBoth
			}
			exact := e.wanted(t, title, mediaType)
			shortTitle := "Harry Potter"
			if scenario == "near blocked" {
				shortTitle = title + " script"
			}
			short := e.wanted(t, shortTitle, models.MediaTypeEbook)
			firstTitle := title
			if scenario == "script first" {
				firstTitle += " script"
			}
			first := filepath.Join(e.libDir, "J. K. Rowling", "a", firstTitle+" - J. K. Rowling.epub")
			second := filepath.Join(e.libDir, "J. K. Rowling", "b", shortTitle+" - J. K. Rowling.epub")
			if scenario != "near blocked" {
				second = filepath.Join(e.libDir, "J. K. Rowling", "b", title+" - J. K. Rowling.epub")
			}
			writeFile(t, first)
			if scenario == "previous scan" || scenario == "near blocked" || scenario == "dual format" {
				if err := e.books.AddBookFile(e.ctx, exact.ID, models.MediaTypeEbook, first); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, second)
			e.s.ScanLibrary(e.ctx)
			if got := e.get(t, exact.ID); got.EbookFilePath != first {
				t.Errorf("exact book took %q, want %q", got.EbookFilePath, first)
			}
			if got := e.get(t, short.ID); got.EbookFilePath != "" {
				t.Errorf("other book took %q", got.EbookFilePath)
			}
			if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 1 {
				t.Errorf("unmatched units = %+v, want one", units)
			}
		})
	}
}

func TestScanLibrary_TitleWriteFailureDoesNotClaimRunnerUp(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	e.books = db.NewBookRepo(database)
	e.authors = db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "hc:rowling", Name: "J. K. Rowling"}
	if err := e.authors.Create(e.ctx, author); err != nil {
		t.Fatal(err)
	}
	e.authorID = author.ID
	e.s = NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), e.books, e.authors,
		db.NewHistoryRepo(database), e.libDir, e.abDir, "", "", "")
	e.s.WithUnmatchedUnits(db.NewUnmatchedUnitRepo(database))
	exact := e.wanted(t, "Harry Potter en het vervloekte kind", models.MediaTypeEbook)
	short := e.wanted(t, "Harry Potter", models.MediaTypeEbook)
	writeFile(t, filepath.Join(e.libDir, "J. K. Rowling", exact.Title+" - J. K. Rowling.epub"))
	if _, err := database.Exec(fmt.Sprintf(`CREATE TRIGGER f BEFORE INSERT ON book_files
  WHEN NEW.book_id = %d BEGIN SELECT RAISE(ABORT,'boom'); END`, exact.ID)); err != nil {
		t.Fatal(err)
	}
	e.s.ScanLibrary(e.ctx)
	for _, id := range []int64{exact.ID, short.ID} {
		if got := e.get(t, id); got.EbookFilePath != "" {
			t.Errorf("book %d took %q", id, got.EbookFilePath)
		}
	}
	if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 1 {
		t.Errorf("unmatched units = %+v, want one", units)
	}
}
