package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/models"
)

// atValueCap returns a value already at the per-value byte cap that
// sanitizePath enforces — the input the composed-segment cap has to survive,
// because it is the largest thing #1982's fix will hand this layer.
func atValueCap(unit string) string {
	v := sanitizePath(strings.Repeat(unit, maxPathComponentBytes))
	if len(v) > maxPathComponentBytes {
		panic(fmt.Sprintf("value cap broken: %d bytes", len(v)))
	}
	return v
}

// TestComposedSegment_DefaultTemplateIsCreatableOnDisk is the reported bug
// (#2014) and it needs no non-ASCII to reproduce: the shipped default template
// renders 200 (title) + 3 (" - ") + 200 (author) + 5 (".epub") = 408 bytes,
// which is past NAME_MAX with both values legal under the per-value cap.
//
// The assertion is a real create, not an arithmetic one, because the failure
// being fixed is ENAMETOOLONG from the kernel rather than a number in a test.
func TestComposedSegment_DefaultTemplateIsCreatableOnDisk(t *testing.T) {
	root := t.TempDir()
	r := NewRenamer("")
	author := &models.Author{Name: atValueCap("x")}
	book := &models.Book{Title: atValueCap("y")}

	dst, err := r.DestPath(root, author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(dst), err)
	}
	f, err := os.Create(dst) // #nosec G304 -- test-local temp dir
	if err != nil {
		t.Fatalf("os.Create(leaf %d bytes): %v", len(filepath.Base(dst)), err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestComposedSegment_KeepsTheExtension pins the reason simple right-truncation
// is not the fix. A name whose ".epub" was chopped off is invisible to every
// format check downstream, so it is worse than the error it replaced.
func TestComposedSegment_KeepsTheExtension(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: atValueCap("x")}
	book := &models.Book{Title: atValueCap("y")}

	dst, err := r.DestPath("/books", author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	if got := filepath.Ext(dst); got != ".epub" {
		t.Errorf("extension = %q, want %q (leaf %q)", got, ".epub", filepath.Base(dst))
	}
	// The " - " glue between the two names is structural too: it is what makes
	// the leaf parseable back into title and author.
	if !strings.Contains(filepath.Base(dst), " - ") {
		t.Errorf("separator glue lost: %q", filepath.Base(dst))
	}
}

// TestComposedSegment_SurvivesStaging covers the confusing half of the report:
// StagedImport writes through stagingPath, which prepends ~35 bytes to the
// leaf, so a destination legal at the final write can still die during
// staging. The budget reserves for it; this proves the reservation is real.
func TestComposedSegment_SurvivesStaging(t *testing.T) {
	root := t.TempDir()
	r := NewRenamer("")
	author := &models.Author{Name: atValueCap("x")}
	book := &models.Book{Title: atValueCap("y")}

	dst, err := r.DestPath(root, author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	staged := stagingPath(dst)
	f, err := os.Create(staged) // #nosec G304 -- test-local temp dir
	if err != nil {
		t.Fatalf("os.Create(staging leaf %d bytes): %v", len(filepath.Base(staged)), err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestComposedSegment_SurvivesUniqueSuffix covers the other appended thing:
// UniqueDir adds " (2)".." (999)" on a collision, so a name that just fits
// imports once and fails the second time the same title arrives.
func TestComposedSegment_SurvivesUniqueSuffix(t *testing.T) {
	root := t.TempDir()
	r := NewRenamer("{Author}/{Title} - {Author}")
	author := &models.Author{Name: atValueCap("x")}
	book := &models.Book{Title: atValueCap("y")}

	dst, err := r.AudiobookDestDir(root, author, book, "", "")
	if err != nil {
		t.Fatalf("AudiobookDestDir: %v", err)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatalf("MkdirAll(first import): %v", err)
	}
	unique := UniqueDir(dst)
	if unique == dst {
		t.Fatalf("UniqueDir did not resolve the collision: %q", unique)
	}
	if err := os.MkdirAll(unique, 0o750); err != nil {
		t.Fatalf("MkdirAll(collision leaf %d bytes): %v", len(filepath.Base(unique)), err)
	}
}

// TestComposedSegment_TruncatesOnRuneBoundaries keeps #1982's guarantee at this
// layer: a name cut mid-encoding leaves invalid UTF-8 in the filename and in
// the book_files path row.
func TestComposedSegment_TruncatesOnRuneBoundaries(t *testing.T) {
	for _, unit := range []string{"é", "転", "𝄞"} {
		t.Run(unit, func(t *testing.T) {
			r := NewRenamer("")
			author := &models.Author{Name: atValueCap(unit)}
			book := &models.Book{Title: atValueCap(unit)}

			dst, err := r.DestPath("/books", author, book, "", "", "book.epub")
			if err != nil {
				t.Fatalf("DestPath: %v", err)
			}
			for _, seg := range strings.Split(strings.TrimPrefix(dst, "/books/"), "/") {
				if !utf8.ValidString(seg) {
					t.Errorf("segment is not valid UTF-8: %q", seg)
				}
				if len(seg) > maxSegmentBytes {
					t.Errorf("segment is %d bytes, over the %d budget: %q",
						len(seg), maxSegmentBytes, seg)
				}
			}
		})
	}
}

// TestComposedSegment_ShortRawTokensSurvive is why the cap shrinks the LONGEST
// value rather than every value: {ext}, {Year} and {Part} are substituted raw,
// outside sanitizePath's per-value cap, and they are the tokens whose loss is
// unrecoverable. Nothing special-cases them by name — being short is enough.
func TestComposedSegment_ShortRawTokensSurvive(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: atValueCap("x")}
	book := &models.Book{Title: atValueCap("y")}

	name := r.AudiobookFileName("{Title} - {Author} - Part {Part:3}.{ext}",
		author, book, "", "", "m4b", 7)
	if len(name) > maxSegmentBytes {
		t.Fatalf("leaf is %d bytes, over the %d budget", len(name), maxSegmentBytes)
	}
	if !strings.HasSuffix(name, " - Part 007.m4b") {
		t.Errorf("raw short tokens lost: %q", name)
	}
}

// TestComposedSegment_LeavesShortNamesAlone is the no-regression guarantee for
// existing libraries: nothing under the budget may move by one byte, or every
// path row in the database stops matching what is on disk.
func TestComposedSegment_LeavesShortNamesAlone(t *testing.T) {
	r := NewRenamer("")
	author := &models.Author{Name: "Test Author"}
	book := &models.Book{Title: "Dark Matter"}

	got, err := r.DestPath("/books", author, book, "", "", "something.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Test Author", "Dark Matter ()",
		"Dark Matter - Test Author.epub")
	if got != want {
		t.Errorf("short name changed:\ngot  %q\nwant %q", got, want)
	}
}

// TestComposedSegment_UnknownGroupIsNotShrunk keeps the literal-passthrough
// contract: "{Titel}" is a typo the renderer preserves verbatim so the user can
// see what they mistyped. Shrinking it would turn a readable mistake into an
// unreadable one.
func TestComposedSegment_UnknownGroupIsNotShrunk(t *testing.T) {
	values := map[string]string{"Title": strings.Repeat("y", maxPathComponentBytes)}
	got := renderSegment("{Titel} {Title}", values)
	if !strings.HasPrefix(got, "{Titel} ") {
		t.Errorf("unknown group did not survive verbatim: %q", got)
	}
	if len(got) > maxSegmentBytes {
		t.Errorf("segment is %d bytes, over the %d budget", len(got), maxSegmentBytes)
	}
}

// TestComposedSegment_LiteralOnlyOverflowIsStillCapped covers the last resort.
// A hand-edited template can put a literal longer than the whole budget in a
// segment; there is no structure left to protect there, but the import must
// still not die with ENAMETOOLONG.
func TestComposedSegment_LiteralOnlyOverflowIsStillCapped(t *testing.T) {
	root := t.TempDir()
	for _, seg := range []string{
		strings.Repeat("L", 400),              // no groups at all
		strings.Repeat("L", 400) + " {Title}", // literal alone over budget
	} {
		got := renderSegment(seg, map[string]string{"Title": "T"})
		if len(got) > maxSegmentBytes {
			t.Fatalf("segment is %d bytes, over the %d budget", len(got), maxSegmentBytes)
		}
		if err := os.MkdirAll(filepath.Join(root, got), 0o750); err != nil {
			t.Fatalf("MkdirAll(%d bytes): %v", len(got), err)
		}
	}
}
