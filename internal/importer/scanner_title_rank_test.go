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
	for _, scenario := range []string{"same scan", "previous scan", "script first", "dual format"} {
		t.Run(scenario, func(t *testing.T) {
			e := newVolumeScanEnv(t, "J. K. Rowling")
			title := "Harry Potter en het vervloekte kind"
			mediaType := models.MediaTypeEbook
			if scenario == "dual format" {
				mediaType = models.MediaTypeBoth
			}
			exact := e.wanted(t, title, mediaType)
			shortTitle := "Harry Potter"
			short := e.wanted(t, shortTitle, models.MediaTypeEbook)
			firstTitle := title
			if scenario == "script first" {
				firstTitle += " script"
			}
			first := filepath.Join(e.libDir, "J. K. Rowling", "a", firstTitle+" - J. K. Rowling.epub")
			second := filepath.Join(e.libDir, "J. K. Rowling", "b", title+" - J. K. Rowling.epub")
			writeFile(t, first)
			if scenario == "previous scan" || scenario == "dual format" {
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

// blockedSibling adds a book of the same author that the scan may not claim:
// "skipped" is a Skipped book, "imported" is a book already holding another
// file. Both stay in the title ranking as blocked hits.
func (e *volumeScanEnv) blockedSibling(t *testing.T, kind, title string) *models.Book {
	t.Helper()
	b := &models.Book{ForeignID: "hc:" + title, AuthorID: e.authorID, Title: title,
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook}
	if kind == "skipped" {
		b.Status = models.BookStatusSkipped
	}
	if err := e.books.Create(e.ctx, b); err != nil {
		t.Fatal(err)
	}
	if kind == "imported" {
		own := filepath.Join(e.libDir, "J. K. Rowling", "owned", title+" - J. K. Rowling.epub")
		writeFile(t, own)
		if err := e.books.AddBookFile(e.ctx, b.ID, models.MediaTypeEbook, own); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// An exact wanted file is claimed even when a blocked near sibling scores
// within the margin: otherwise the wanted book stays Wanted and is downloaded
// again although its file is in the library. On main only wanted books were
// candidates, so the sibling never mattered.
func TestScanLibrary_ExactWantedFileBeatsBlockedNearSibling(t *testing.T) {
	for _, kind := range []string{"skipped", "imported"} {
		t.Run(kind, func(t *testing.T) {
			e := newVolumeScanEnv(t, "J. K. Rowling")
			title := "Harry Potter en het vervloekte kind"
			wanted := e.wanted(t, title, models.MediaTypeEbook)
			sibling := e.blockedSibling(t, kind, title+" script")
			siblingBefore := e.get(t, sibling.ID).EbookFilePath
			file := filepath.Join(e.libDir, "J. K. Rowling", title+" - J. K. Rowling.epub")
			writeFile(t, file)

			e.s.ScanLibrary(e.ctx)

			if got := e.get(t, wanted.ID); got.EbookFilePath != file {
				t.Errorf("wanted book EbookFilePath = %q, want %q", got.EbookFilePath, file)
			}
			if got := e.get(t, sibling.ID); got.EbookFilePath != siblingBefore {
				t.Errorf("sibling file changed from %q to %q", siblingBefore, got.EbookFilePath)
			}
			if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 0 {
				t.Errorf("unmatched units = %+v, want none", units)
			}
		})
	}
}

// A tie at 1.0 between a wanted book and a blocked book with the same
// normalised title is not strictly ahead, so the file is left alone.
func TestScanLibrary_ExactTieWithBlockedBookStaysUnmatched(t *testing.T) {
	e := newVolumeScanEnv(t, "Jane Doe")
	writeFile(t, filepath.Join(e.libDir, "Jane Doe", "The Fragile Threads of Power - Jane Doe.epub"))
	wanted := e.wanted(t, "Fragile Threads of Power, The", models.MediaTypeEbook)
	skipped := &models.Book{ForeignID: "hc:dup", AuthorID: e.authorID, Title: "The Fragile Threads of Power",
		Status: models.BookStatusSkipped, MediaType: models.MediaTypeEbook}
	if err := e.books.Create(e.ctx, skipped); err != nil {
		t.Fatal(err)
	}

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, wanted.ID); got.EbookFilePath != "" {
		t.Errorf("wanted book took %q despite a tie with a blocked book", got.EbookFilePath)
	}
	if got := e.get(t, skipped.ID); got.EbookFilePath != "" {
		t.Errorf("skipped book took %q", got.EbookFilePath)
	}
	if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 1 {
		t.Errorf("unmatched units = %d, want 1", len(units))
	}
}

// Without an exact title the blocked near sibling still holds the file back:
// the wanted book leads by under the margin and the sibling cannot take it.
func TestScanLibrary_NonExactWithBlockedNearStaysUnmatched(t *testing.T) {
	for _, kind := range []string{"skipped", "imported"} {
		t.Run(kind, func(t *testing.T) {
			e := newVolumeScanEnv(t, "J. K. Rowling")
			title := "Harry Potter en het vervloekte kind"
			wanted := e.wanted(t, title+" script", models.MediaTypeEbook)
			sibling := e.blockedSibling(t, kind, title)
			siblingBefore := e.get(t, sibling.ID).EbookFilePath
			writeFile(t, filepath.Join(e.libDir, "J. K. Rowling", title+" scripts - J. K. Rowling.epub"))

			e.s.ScanLibrary(e.ctx)

			if got := e.get(t, wanted.ID); got.EbookFilePath != "" {
				t.Errorf("wanted book took %q", got.EbookFilePath)
			}
			if got := e.get(t, sibling.ID); got.EbookFilePath != siblingBefore {
				t.Errorf("sibling file changed to %q", got.EbookFilePath)
			}
			if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 1 {
				t.Errorf("unmatched units = %+v, want one", units)
			}
		})
	}
}

// Numbered titles are told apart by libraryVolumeConflict, so a file for
// volume 2 claims volume 2 and never the near-identical volume 1 (the
// FindExisting counterpart groups numbers-only differences the same way).
func TestScanLibrary_NumberedTitlesClaimTheirOwnVolume(t *testing.T) {
	e := newVolumeScanEnv(t, "Jane Doe")
	file := filepath.Join(e.libDir, "Jane Doe", "Saga of the Reach 2 - Jane Doe.epub")
	writeFile(t, file)
	one := e.wanted(t, "Saga of the Reach 1", models.MediaTypeEbook)
	two := e.wanted(t, "Saga of the Reach 2", models.MediaTypeEbook)

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, two.ID); got.EbookFilePath != file {
		t.Errorf("volume 2 EbookFilePath = %q, want %q", got.EbookFilePath, file)
	}
	if got := e.get(t, one.ID); got.EbookFilePath != "" {
		t.Errorf("volume 1 took %q", got.EbookFilePath)
	}
}

// A book already claimed by an earlier file of the pass still marks a later
// file as its sidecar when that file sits outside the book's effective root
// (#2188): the file is counted as tracked, not listed as unmatched. The root
// check turns the file away before the book becomes a hit, so the claim has
// to be noted there.
func TestScanLibrary_ClaimedBookOutsideRootStillMakesASidecar(t *testing.T) {
	e := newVolumeScanEnv(t, "J. K. Rowling")
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	e.books = db.NewBookRepo(database)
	e.authors = db.NewAuthorRepo(database)
	rootRepo := db.NewRootFolderRepo(database)
	authorRoot := filepath.Join(e.libDir, "J. K. Rowling", "vol1")
	rf, err := rootRepo.Create(e.ctx, authorRoot)
	if err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:rowling", Name: "J. K. Rowling", RootFolderID: &rf.ID}
	if err := e.authors.Create(e.ctx, author); err != nil {
		t.Fatal(err)
	}
	e.authorID = author.ID
	e.s = NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), e.books, e.authors,
		db.NewHistoryRepo(database), e.libDir, e.abDir, "", "", "").WithRootFolders(rootRepo)
	e.s.WithUnmatchedUnits(db.NewUnmatchedUnitRepo(database))
	book := e.wanted(t, "Harry Potter en het vervloekte kind", models.MediaTypeEbook)
	epub := filepath.Join(authorRoot, book.Title+" - J. K. Rowling.epub")
	notes := filepath.Join(e.libDir, "J. K. Rowling", "vol2", book.Title+" - J. K. Rowling.txt")
	writeFile(t, epub)
	writeFile(t, notes)
	if order := walkOrder(t, e.libDir); len(order) != 2 || order[0] != epub {
		t.Fatalf("walk order = %v, want the epub first", order)
	}

	e.s.ScanLibrary(e.ctx)

	if got := e.get(t, book.ID); got.EbookFilePath != epub {
		t.Fatalf("EbookFilePath = %q, want %q", got.EbookFilePath, epub)
	}
	if units := readUnmatchedFiles(t, e.ctx, e.s); len(units) != 0 {
		t.Errorf("unmatched units = %+v, want none (the txt is the epub's sidecar)", units)
	}
}
