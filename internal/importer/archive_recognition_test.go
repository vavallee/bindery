package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsArchivePart_ZipAndSevenZip(t *testing.T) {
	for name, want := range map[string]bool{
		"x.zip": true, "x.ZIP": true, "x.7z": true, "x.7z.001": true, "x.7Z.012": true,
		// Book files stay book files: a .cbz and an .epub are zips by format only.
		"x.epub": false, "x.cbz": false, "x.cbr": false, "x.7zip": false, "x.7z.1": false, "zip": false,
	} {
		if got := isArchivePart(name); got != want {
			t.Errorf("isArchivePart(%q) = %v, want %v", name, got, want)
		}
		if got := isDownloadArtifact(name); got != want && !isReceiptOrRepairFile(name) {
			t.Errorf("isDownloadArtifact(%q) = %v, want %v (kept out of copy and hardlink placements)", name, got, want)
		}
	}
}

func TestIsBookFile_CbzAndEpubAreNotArchives(t *testing.T) {
	for _, name := range []string{"a.epub", "a.cbz", "a.azw3", "a.mobi", "a.pdf", "a.m4b", "a.mp3"} {
		if isArchivePart(name) {
			t.Errorf("%s is a book extension but isArchivePart says archive", name)
		}
	}
}

func TestArchiveWaitMessage_NamesTheArchiveType(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"/d/a.zip", "this download holds only an archive (ZIP)."},
		{"/d/a.7z", "this download holds only an archive (7z), and Bindery does not unpack archives"},
		{"/d/a.7z.001", "this download holds only an archive (7z), and Bindery does not unpack archives"},
		{"/d/a.rar", "this download holds only an archive, and Bindery does not unpack archives"},
	} {
		msg := archiveWaitMessage(tc.file, "/d", "")
		if !strings.Contains(msg, "waiting for "+filepath.Base(tc.file)+" to be extracted") || !strings.Contains(msg, tc.want) || !strings.Contains(msg, "unpackerr") {
			t.Errorf("%s: %q", tc.file, msg)
		}
	}
}

// A 7z or ZIP only folder waits exactly like a RAR one: no book, no retry
// spent, no "no book files found".
func TestArchiveAwaitingExtraction_ZipAndSevenZip(t *testing.T) {
	for _, name := range []string{"a.zip", "a.7z", "a.7z.001"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := archiveAwaitingExtraction(dir); filepath.Base(got) != name {
			t.Errorf("%s: archiveAwaitingExtraction = %q", name, got)
		}
		if importSourcePresent(dir, nil) {
			t.Errorf("%s: counts as present, so every poll would spend a retry", name)
		}
		reason := importSourceGoneReason(dir)
		if !strings.Contains(reason, name) || strings.Contains(reason, "no book files") {
			t.Errorf("%s: reason %q", name, reason)
		}
	}
}

// A .cbz or an .epub in the folder is a book, so nothing is waiting.
func TestArchiveAwaitingExtraction_CbzAndEpubAreBooks(t *testing.T) {
	for _, name := range []string{"a.cbz", "a.epub"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", int(MinPlausibleEbookBytes)+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := archiveAwaitingExtraction(dir); got != "" {
			t.Errorf("%s: archiveAwaitingExtraction = %q, want none", name, got)
		}
	}
}
