package importer

import (
	"path/filepath"
	"testing"

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
