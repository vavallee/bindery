package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestDrop_ManualImportOfATinyFileIsPlaced is the manual override under an
// external drop folder (#3224): a 3000 byte file that really is the book is
// handed off, as it is in managed mode.
func TestDrop_ManualImportOfATinyFileIsPlaced(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})
	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "short.txt")
	if err := os.WriteFile(src, []byte(strings.Repeat("x", 3000)), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.ImportFromPath(ctx, dl, downloadDir, "")

	findOneFile(t, dropDir)
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// TestDrop_AutomaticImportOfOnlyTinyFilesIsBlocked: an automatic drop of a
// download holding nothing but notes is blocked with the manual import
// pointer, not "no book files found", and nothing is dropped.
func TestDrop_AutomaticImportOfOnlyTinyFilesIsBlocked(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})
	downloadDir := t.TempDir()
	writeNote(t, filepath.Join(downloadDir, "www.site.txt"))
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	assertEmptyDir(t, dropDir, "drop folder")
	got, err := downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked || !strings.Contains(got.ErrorMessage, "manual import") {
		t.Errorf("status %q message %q, want blocked and a manual import pointer", got.Status, got.ErrorMessage)
	}
}

// TestDrop_AutomaticImportLeavesNotesBesideTheBook: the notes file is not
// dropped as a second format.
func TestDrop_AutomaticImportLeavesNotesBesideTheBook(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})
	downloadDir := t.TempDir()
	writeEbook(t, filepath.Join(downloadDir, "Title T.epub"), "book")
	writeNote(t, filepath.Join(downloadDir, "Title T.txt"))
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	if got := findOneFile(t, dropDir); !strings.HasSuffix(got, ".epub") {
		t.Errorf("dropped %q, want the epub only", got)
	}
}

// TestDrop_TemplatedLayoutKeepsTheMatchingBook: the destination check covers
// the templated layout as well as the flat one.
func TestDrop_TemplatedLayoutKeepsTheMatchingBook(t *testing.T) {
	s, _, _, _, _, _, book, _ := dropFixture(t, models.MediaTypeEbook)
	book.Title = "Origin"
	downloadPath := t.TempDir()
	writeEbook(t, filepath.Join(downloadPath, "Before the Origin (2)", "Before the Origin - Author A.epub"), "before")
	writeEbook(t, filepath.Join(downloadPath, "Origin (1)", "Origin - Author A.epub"), "origin")
	folder := t.TempDir()

	_, _, err := s.placeDroppedFormat(t.Context(), book, &models.Author{Name: "Author A"}, downloadPath, discoverBookFiles(t.Context(), downloadPath, nil), nil, models.MediaTypeEbook, folder, "templated", "copy")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(findOneFile(t, folder))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "origin") {
		t.Errorf("the dropped file is not the Origin EPUB")
	}
}
