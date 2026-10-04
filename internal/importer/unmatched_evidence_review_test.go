package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestScanLibrary_NarratorArtistIsNotAConflict: a correctly placed
// James Patterson/Kiss the Girls/ whose tracks carry the narrator in Artist
// (TPE1 "Scott Brick") and the author in Album Artist (TPE2). The author tag
// that agrees with the folder settles it: no conflict, the folder author's
// book is the suggestion, and the reason is not "author not in library".
func TestScanLibrary_NarratorArtistIsNotAConflict(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	jp := &models.Author{ForeignID: "ol:patterson", Name: "James Patterson", SortName: "Patterson, James", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, jp); err != nil {
		t.Fatal(err)
	}
	// Skipped, so the scan leaves the files for a person to adopt.
	kiss := &models.Book{ForeignID: "ol:kiss", AuthorID: jp.ID, Title: "Kiss the Girls", Status: models.BookStatusSkipped,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, kiss); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(libraryDir, "James Patterson", "Kiss the Girls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		data := layoutID3([2]string{"TPE1", "Scott Brick"}, [2]string{"TPE2", "James Patterson"}, [2]string{"TALB", "Kiss the Girls"})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.ScanLibrary(ctx)

	u := onlyUnit(t, ctx, s)
	if u.Reason == unmatchedReasonAuthorNotInLibrary {
		t.Errorf("reason = %q: the folder's author is in the library and named by Album Artist", u.Reason)
	}
	if len(u.Candidates) == 0 || u.Candidates[0].BookID != kiss.ID {
		t.Fatalf("candidates = %+v, want Kiss the Girls first", u.Candidates)
	}
	if u.ParsedTitle != "Kiss the Girls" {
		t.Errorf("parsed title = %q", u.ParsedTitle)
	}
	if u.FilesAuthor != "" || u.Candidates[0].FolderAuthorOnly {
		t.Errorf("files author = %q, folder only = %v: want no conflict", u.FilesAuthor, u.Candidates[0].FolderAuthorOnly)
	}
}

// TestScanLibrary_TaggedFilesAreNotAttachedToTheFolderBook: the #2942 tracks in
// a folder named after a Patterson book that is in the library and wanted. The
// book folder title tier used to fall back to the folder's author and attach
// all seven Katy Evans tracks to it. Tags that name only another author keep
// the folder's book out of reach, so the unit waits on Import instead.
func TestScanLibrary_TaggedFilesAreNotAttachedToTheFolderBook(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	pattersonCatalogue(t, ctx, authors, books)
	jp, err := authors.GetByForeignID(ctx, "ol:patterson")
	if err != nil || jp == nil {
		t.Fatalf("patterson: %v", err)
	}
	folderBook := &models.Book{ForeignID: "ol:10m", AuthorID: jp.ID, Title: evansFolder, Status: models.BookStatusWanted,
		Monitored: true, MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, folderBook); err != nil {
		t.Fatal(err)
	}
	writeEvansTracks(t, libraryDir, true)

	s.ScanLibrary(ctx)

	paths, err := books.ListAllBookFilePaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("book files = %v, want none: Katy Evans' tracks are not %q", paths, evansFolder)
	}
	u := onlyUnit(t, ctx, s)
	if u.FileCount != 7 || u.ParsedAuthor != evansAuthor || u.FilesAuthor != evansAuthor {
		t.Errorf("unit = %d files by %q (files author %q), want 7 by %q", u.FileCount, u.ParsedAuthor, u.FilesAuthor, evansAuthor)
	}
	// Scored on "Tycoon", the folder's book is no suggestion at all.
	if len(u.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none", u.Candidates)
	}
}
