package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// seriesDownloadFixture seeds the ebook "Origin" and a download for it,
// returning the scanner, the download and a directory the caller fills.
func seriesDownloadFixture(t *testing.T) (*Scanner, *models.Download, string, func() []models.BookFile) {
	t.Helper()
	s, dl, _, bookRepo, ctx := dataLossFixture(t, t.TempDir(), "copy")
	book, err := bookRepo.GetByID(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	book.Title = "Origin"
	if err := bookRepo.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	// A name the pack markers do not catch. One they catch is refused before
	// any file is placed (#2276, #3224).
	dl.Title = "Origin - Author A EPUB"
	files := func() []models.BookFile {
		got, err := bookRepo.ListBookFiles(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	return s, dl, t.TempDir(), files
}

// writeEbook writes an EPUB-shaped file tagged with marker, so the test can
// tell which source landed.
func writeEbook(t *testing.T, path, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	body := append(epubBytes(), bytes.Repeat([]byte(marker), 1024)...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestImport_SeriesDownloadPlacesTheMatchingBook is #3224. A download holding
// a whole series gives every EPUB the same destination, and the first file in
// walk order used to take it: "Before the Origin (2)/" sorts before
// "Origin (1)/", so the book Origin was filed with another novel.
func TestImport_SeriesDownloadPlacesTheMatchingBook(t *testing.T) {
	s, dl, downloadPath, files := seriesDownloadFixture(t)
	writeEbook(t, filepath.Join(downloadPath, "Before the Origin (2)", "Before the Origin - Author A.epub"), "before")
	writeEbook(t, filepath.Join(downloadPath, "Origin (1)", "Origin - Author A.epub"), "origin")
	writeEbook(t, filepath.Join(downloadPath, "Origin and Beyond (3)", "Origin and Beyond - Author A.epub"), "beyond")

	s.tryImportInternal(t.Context(), dl, downloadPath, "", "", "", nil, nil)

	got := files()
	if len(got) != 1 {
		t.Fatalf("book files = %d, want 1: %+v", len(got), got)
	}
	data, err := os.ReadFile(got[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("origin")) || bytes.Contains(data, []byte("before")) {
		t.Errorf("the library file is not the Origin EPUB from the download")
	}
}

// TestImport_SeriesDownloadMatchesByEpubTitle covers releases whose file names
// say nothing about the book: the EPUB's dc:title decides which one is placed.
func TestImport_SeriesDownloadMatchesByEpubTitle(t *testing.T) {
	s, dl, downloadPath, files := seriesDownloadFixture(t)
	writeEpubAt(t, filepath.Join(downloadPath, "a.epub"), "Before the Origin", "Author A", "9780000000001")
	writeEpubAt(t, filepath.Join(downloadPath, "b.epub"), "Origin", "Author A", "9780000000002")

	s.tryImportInternal(t.Context(), dl, downloadPath, "", "", "", nil, nil)

	got := files()
	if len(got) != 1 {
		t.Fatalf("book files = %d, want 1: %+v", len(got), got)
	}
	meta, err := ReadEpubMetadata(got[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Origin" {
		t.Errorf("placed EPUB has dc:title %q, want %q", meta.Title, "Origin")
	}
}
