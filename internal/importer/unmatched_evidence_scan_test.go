package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// The #2942 layout: a Katy Evans audiobook ("Tycoon", seven mp3 tracks) left
// in a James Patterson book folder. The folder names one author, the file
// names and tags another.
const (
	evansFolder = "$10,000,000 Marriage Proposition"
	evansAuthor = "Katy Evans"
	evansTitle  = "Tycoon"
)

// pattersonCatalogue adds James Patterson with the three books #2942's
// suggestions were drawn from, and returns their ids by title.
func pattersonCatalogue(t *testing.T, ctx context.Context, authors *db.AuthorRepo, books *db.BookRepo) map[string]int64 {
	t.Helper()
	jp := &models.Author{ForeignID: "ol:patterson", Name: "James Patterson", SortName: "Patterson, James", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, jp); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	for _, title := range []string{"Katt vs. Dogg", "Katt Loves Dogg", "Private Monaco"} {
		b := &models.Book{ForeignID: "ol:" + title, AuthorID: jp.ID, Title: title, Status: models.BookStatusWanted,
			Monitored: true, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		ids[title] = b.ID
	}
	return ids
}

// writeEvansTracks lays out the seven "Katy Evans - Tycoon N-7.mp3" tracks in
// the Patterson folder. tagged gives each the tags from the report: title
// "Katy Evans - Tycoon", artist "Katy Evans", album "Tycoon".
func writeEvansTracks(t *testing.T, libraryDir string, tagged bool) {
	t.Helper()
	dir := filepath.Join(libraryDir, "James Patterson", evansFolder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 7; i++ {
		data := []byte("audio")
		if tagged {
			data = layoutID3([2]string{"TIT2", evansAuthor + " - " + evansTitle}, [2]string{"TPE1", evansAuthor}, [2]string{"TALB", evansTitle})
		}
		name := fmt.Sprintf("%s - %s %d-7.mp3", evansAuthor, evansTitle, i)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func onlyUnit(t *testing.T, ctx context.Context, s *Scanner) db.UnmatchedUnit {
	t.Helper()
	units := readUnmatchedFiles(t, ctx, s)
	if len(units) != 1 {
		t.Fatalf("units = %+v, want exactly one", units)
	}
	return units[0]
}

// TestScanLibrary_TaggedFilesOfAnotherAuthorAreNotSuggestedToTheFolderAuthor
// is #2942 as reported. The tags name Katy Evans and the album "Tycoon"; the
// folder says James Patterson. The suggestions used to be Patterson titles
// scored against the track title "Katy Evans - Tycoon", whose "Kat" prefix put
// "Katt vs. Dogg" at 80%. Scored against the album the files name, no
// Patterson title is anywhere near, and the unit names the author the files
// name, who is not in the library.
func TestScanLibrary_TaggedFilesOfAnotherAuthorAreNotSuggestedToTheFolderAuthor(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	pattersonCatalogue(t, ctx, authors, books)
	writeEvansTracks(t, libraryDir, true)

	s.ScanLibrary(ctx)

	u := onlyUnit(t, ctx, s)
	if u.FileCount != 7 {
		t.Fatalf("file count = %d, want 7", u.FileCount)
	}
	if len(u.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: no Patterson title is close to %q", u.Candidates, evansTitle)
	}
	if u.ParsedAuthor != evansAuthor || u.ParsedTitle != evansTitle {
		t.Errorf("parsed = %q by %q, want %q by %q", u.ParsedTitle, u.ParsedAuthor, evansTitle, evansAuthor)
	}
	if u.Reason != unmatchedReasonAuthorNotInLibrary {
		t.Errorf("reason = %q, want %q", u.Reason, unmatchedReasonAuthorNotInLibrary)
	}
}

// TestScanLibrary_FileNamesOfAnotherAuthorAreEvidence: the same tracks with no
// tags at all. Seven files agreeing on "Katy Evans - Tycoon N-7" name the
// author and the book, and the unit says so rather than reporting the folder
// author as what the files parsed to.
func TestScanLibrary_FileNamesOfAnotherAuthorAreEvidence(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	pattersonCatalogue(t, ctx, authors, books)
	writeEvansTracks(t, libraryDir, false)

	s.ScanLibrary(ctx)

	u := onlyUnit(t, ctx, s)
	if u.ParsedAuthor != evansAuthor || u.ParsedTitle != evansTitle {
		t.Errorf("parsed = %q by %q, want %q by %q", u.ParsedTitle, u.ParsedAuthor, evansTitle, evansAuthor)
	}
	if u.Reason != unmatchedReasonAuthorNotInLibrary {
		t.Errorf("reason = %q, want %q", u.Reason, unmatchedReasonAuthorNotInLibrary)
	}
	if len(u.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none", u.Candidates)
	}
}

// TestScanLibrary_EvidencedAuthorInLibraryLeadsTheSuggestions: when the author
// the files name is in the library, their book is the suggestion, ranked by
// the title the files name, ahead of anything by the folder's author. With
// only the file names to go on the folder author's books used to be the only
// ones searched.
func TestScanLibrary_EvidencedAuthorInLibraryLeadsTheSuggestions(t *testing.T) {
	for _, tagged := range []bool{true, false} {
		t.Run(fmt.Sprintf("tagged=%v", tagged), func(t *testing.T) {
			s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
			pattersonCatalogue(t, ctx, authors, books)
			ke := &models.Author{ForeignID: "ol:evans", Name: evansAuthor, SortName: "Evans, Katy", MetadataProvider: "openlibrary"}
			if err := authors.Create(ctx, ke); err != nil {
				t.Fatal(err)
			}
			// Skipped, so the scan does not claim it by itself; a person may.
			tycoon := &models.Book{ForeignID: "ol:tycoon", AuthorID: ke.ID, Title: evansTitle, Status: models.BookStatusSkipped,
				MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
			if err := books.Create(ctx, tycoon); err != nil {
				t.Fatal(err)
			}
			writeEvansTracks(t, libraryDir, tagged)

			s.ScanLibrary(ctx)

			u := onlyUnit(t, ctx, s)
			if len(u.Candidates) == 0 || u.Candidates[0].BookID != tycoon.ID || u.Candidates[0].Score != 1 {
				t.Fatalf("candidates = %+v, want %s (book %d) first at 1.0", u.Candidates, evansTitle, tycoon.ID)
			}
			if u.ParsedAuthor != evansAuthor {
				t.Errorf("parsed author = %q, want %q", u.ParsedAuthor, evansAuthor)
			}
		})
	}
}

// TestScanLibrary_AgreeingTagAuthorKeepsTheOldSuggestions: tags naming the
// folder's own author change nothing. The unit is scored on the title it
// always was, and the folder author's look alike is still offered.
func TestScanLibrary_AgreeingTagAuthorKeepsTheOldSuggestions(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	ids := pattersonCatalogue(t, ctx, authors, books)
	dir := filepath.Join(libraryDir, "James Patterson", "Katt Goes West")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		data := layoutID3([2]string{"TIT2", fmt.Sprintf("Chapter %d", i)}, [2]string{"TPE1", "James Patterson"}, [2]string{"TALB", "Katt Goes West"})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("James Patterson - Katt Goes West %02d.mp3", i)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	u := onlyUnit(t, ctx, s)
	if u.ParsedAuthor != "James Patterson" || u.ParsedTitle != "Katt Goes West" {
		t.Errorf("parsed = %q by %q", u.ParsedTitle, u.ParsedAuthor)
	}
	if len(u.Candidates) == 0 || u.Candidates[0].BookID != ids["Katt vs. Dogg"] && u.Candidates[0].BookID != ids["Katt Loves Dogg"] {
		t.Fatalf("candidates = %+v, want a Katt title first", u.Candidates)
	}
	if u.Reason != unmatchedReasonNoTitleMatch {
		t.Errorf("reason = %q, want %q", u.Reason, unmatchedReasonNoTitleMatch)
	}
}
