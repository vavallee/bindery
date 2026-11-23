package importer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImport_PreferredFormatImportedLastIsRendered is the #4142 repro: one
// download holding a PDF, an AZW3, a MOBI and an EPUB, imported in that order.
// Each file's book_files row is written before the file is promoted to its
// destination, and the refresh that write triggers only counts files on disk,
// so the file being imported never counted and the one imported last could not
// win. The book ended up rendering the AZW3 until the next library scan.
func TestImport_PreferredFormatImportedLastIsRendered(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)

	// The import walks the download folder in name order, so the numeric
	// prefixes fix the order the report describes.
	order := []string{".pdf", ".azw3", ".mobi", ".epub"}
	for i, ext := range order {
		name := string(rune('1'+i)) + " Defiance of the Fall 3" + ext
		if err := os.WriteFile(filepath.Join(f.dir, name), bookSized("volume three "+ext), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	files := f.files(t, f.right.ID)
	if len(files) != len(order) {
		t.Fatalf("book has %d files (%+v), want %d", len(files), files, len(order))
	}
	// Guard the premise: the EPUB must really have been imported last, or the
	// assertion below would pass without the fix.
	for i, ext := range order {
		if got := strings.ToLower(filepath.Ext(files[i].Path)); got != ext {
			t.Fatalf("file %d was imported as %s, want %s: the import order is not the one under test (%+v)", i, got, ext, files)
		}
		if _, err := os.Stat(files[i].Path); err != nil {
			t.Fatalf("imported file missing on disk: %v", err)
		}
	}
	if got := strings.ToLower(filepath.Ext(book.EbookFilePath)); got != ".epub" {
		t.Errorf("book renders %q, want the EPUB: the format imported last was not considered", book.EbookFilePath)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want %q", book.Status, models.BookStatusImported)
	}
}

// TestRefreshAfterCommit_FailureIsLoggedNotFatal: the file and its row are in
// place by the time the refresh runs, so a refresh that cannot run is logged
// and the import carries on.
func TestRefreshAfterCommit_FailureIsLoggedNotFatal(t *testing.T) {
	// Not parallel: swaps the default logger.
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.scanner.refreshAfterCommit(ctx, f.right.ID, "/library/book.epub")

	if !strings.Contains(buf.String(), "failed to refresh book paths after import") {
		t.Errorf("a failed refresh was not logged; log was %q", buf.String())
	}
}
