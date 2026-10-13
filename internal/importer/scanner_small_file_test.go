package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// writeNote writes a 253 byte text file, the size of the release notes in #3224.
func writeNote(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 253)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestImport_SeriesDownloadSkipsTheReleaseNotes is #3224 end to end: ten book
// folders and two 253 byte notes at the root. One EPUB, the matching one, is
// placed, and no note is tracked as a second format.
func TestImport_SeriesDownloadSkipsTheReleaseNotes(t *testing.T) {
	s, dl, downloadPath, files := seriesDownloadFixture(t)
	writeEbook(t, filepath.Join(downloadPath, "Before the Origin (2)", "Before the Origin - Author A.epub"), "before")
	writeEbook(t, filepath.Join(downloadPath, "Origin (1)", "Origin - Author A.epub"), "origin")
	writeNote(t, filepath.Join(downloadPath, "Free Books Online Must See.txt"))
	writeNote(t, filepath.Join(downloadPath, "Origin - Author A.txt"))

	s.tryImportInternal(t.Context(), dl, downloadPath, "", "", "", nil, nil)

	got := files()
	if len(got) != 1 {
		t.Fatalf("book files = %d, want 1: %+v", len(got), got)
	}
	if !strings.HasSuffix(got[0].Path, ".epub") {
		t.Errorf("tracked %q, want the EPUB only", got[0].Path)
	}
}

// TestImport_OnlyTinyEbookFilesBlocksTheImport: a download whose only ebook
// files are notes is not retried, and nothing is placed.
func TestImport_OnlyTinyEbookFilesBlocksTheImport(t *testing.T) {
	s, dl, downloadPath, files := seriesDownloadFixture(t)
	writeNote(t, filepath.Join(downloadPath, "readme.txt"))

	s.tryImportInternal(t.Context(), dl, downloadPath, "", "", "", nil, nil)

	if got := files(); len(got) != 0 {
		t.Fatalf("book files = %+v, want none", got)
	}
	got, err := s.downloads.GetByID(t.Context(), dl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Errorf("state = %q, want %q (error %q)", got.Status, models.StateImportBlocked, got.ErrorMessage)
	}
}

// TestImport_ManualImportKeepsTinyFiles: manual import is the override for a
// book that small, as TooSmallToBeABook documents.
func TestImport_ManualImportKeepsTinyFiles(t *testing.T) {
	s, dl, downloadPath, files := seriesDownloadFixture(t)
	writeNote(t, filepath.Join(downloadPath, "Origin - Author A.txt"))

	s.ImportFromPath(t.Context(), dl, downloadPath, "")

	if got := files(); len(got) != 1 {
		t.Fatalf("book files = %d, want the one tiny file kept: %+v", len(got), got)
	}
}

// TestDiscoverBookFiles_TooSmall judges ebook formats only. A short audio clip
// is a track, not a note, so it stays, as does an ebook at the floor.
func TestDiscoverBookFiles_TooSmall(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, filepath.Join(dir, "notes.txt"))
	writeNote(t, filepath.Join(dir, "tiny.epub"))
	writeNote(t, filepath.Join(dir, "clip.mp3"))
	writeNote(t, filepath.Join(dir, "clip.m4b"))
	atFloor := filepath.Join(dir, "floor.epub")
	if err := os.WriteFile(atFloor, make([]byte, MinPlausibleEbookBytes), 0o644); err != nil {
		t.Fatal(err)
	}

	kept, dropped := discoverBookFilesSized(dir, nil, true)
	keptNames := map[string]bool{}
	for _, p := range kept {
		keptNames[filepath.Base(p)] = true
	}
	for _, want := range []string{"clip.mp3", "clip.m4b", "floor.epub"} {
		if !keptNames[want] {
			t.Errorf("%s was dropped, kept %v", want, kept)
		}
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %v, want notes.txt and tiny.epub", dropped)
	}

	all, none := discoverBookFilesSized(dir, nil, false)
	if len(all) != 5 || len(none) != 0 {
		t.Errorf("without the check: kept %d dropped %d, want 5 and 0", len(all), len(none))
	}

	// The download client's explicit list is judged the same way.
	explicit := []string{filepath.Join(dir, "notes.txt"), atFloor}
	kept, dropped = discoverBookFilesSized(dir, explicit, true)
	if len(kept) != 1 || len(dropped) != 1 {
		t.Errorf("explicit list: kept %v dropped %v", kept, dropped)
	}
}

// TestDropFlatLayoutKeepsTheMatchingBook keeps the file closest to the book in the flat drop
// layout, where every file of a format shares one name (#3224).
func TestDropFlatLayoutKeepsTheMatchingBook(t *testing.T) {
	s, _, downloadPath, _ := seriesDownloadFixture(t)
	writeEbook(t, filepath.Join(downloadPath, "Origin (1)", "Origin - Author A.epub"), "origin")
	writeEbook(t, filepath.Join(downloadPath, "Origin and Beyond (3)", "Origin and Beyond - Author A.epub"), "beyond")
	writeEbook(t, filepath.Join(downloadPath, "Zeta", "Zeta - Author A.epub"), "zeta")
	folder := t.TempDir()
	book := &models.Book{Title: "Origin"}
	author := &models.Author{Name: "Author A"}

	_, _, err := s.placeDroppedFormat(t.Context(), book, author, downloadPath, discoverBookFiles(t.Context(), downloadPath, nil), nil, models.MediaTypeEbook, folder, "flat", "copy")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 1 {
		t.Fatalf("drop folder = %v, %v; want one file", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(folder, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "origin") || strings.Contains(string(data), "zeta") {
		t.Errorf("dropped %q is not the Origin EPUB", entries[0].Name())
	}
}

// TestDiscoverBookFiles_ManualContextKeepsTinyFiles: the context decides. The
// formats the other callers read (the import mode, the preview) come out the
// same either way, so the placement and pair gating tests are what show their
// effect; this pins the shared function itself.
func TestDiscoverBookFiles_ManualContextKeepsTinyFiles(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, filepath.Join(dir, "short.txt"))
	if got := discoverBookFiles(t.Context(), dir, nil); len(got) != 0 {
		t.Errorf("automatic context kept %v", got)
	}
	if got := discoverBookFiles(withManualImport(t.Context()), dir, nil); len(got) != 1 {
		t.Errorf("manual context kept %v, want the tiny file", got)
	}
	explicit := []string{filepath.Join(dir, "short.txt")}
	if got := discoverBookFiles(withManualImport(t.Context()), dir, explicit); len(got) != 1 {
		t.Errorf("manual context with an explicit list kept %v", got)
	}
}
