package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// soulseekFixture is a content fixture whose download came from Soulseek.
func soulseekFixture(t *testing.T) *contentFixture {
	t.Helper()
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.dl.Protocol = "soulseek"
	f.dl.MediaType = models.MediaTypeEbook
	f.dl.GUID = "slskd:0123456789abcdef0123456789abcdef"
	return f
}

// A file that is another book is only noted for a torrent in the default warn
// mode, and refused for Soulseek, where nothing vouches for it. Mutation check:
// make strictOr return its input and this fails on the Soulseek leg.
func TestSoulseekBlockMode_RefusesAWrongBookInTheDefaultWarnMode(t *testing.T) {
	torrent := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	torrent.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	torrent.importDownload(t)
	if dl, _ := torrent.reload(t); dl.Status != models.StateImported {
		t.Fatalf("control: a torrent in warn mode should import, got %q", dl.Status)
	}

	f := soulseekFixture(t)
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	for _, want := range []string{"Mary and Mr Eliot", "Mary, Mary", "Match to book"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("message %q does not name %q", dl.ErrorMessage, want)
		}
	}
	if book.Status != models.BookStatusWanted || f.bookFileCount(t) != 0 {
		t.Errorf("book status %q, %d files: nothing may be placed", book.Status, f.bookFileCount(t))
	}
	// A Soulseek result has no indexer; the blocklist is keyed on its GUID, which
	// hashes the peer and the files, and the entry carries no indexer.
	if !f.blocklisted(t) {
		t.Error("the Soulseek release was not blocklisted")
	}
	entries, err := f.blocklist.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IndexerID != nil || entries[0].GUID != f.dl.GUID {
		t.Errorf("blocklist = %+v", entries)
	}
	if dl.ImportPath == "" {
		t.Error("the folder was not recorded, so Match to book cannot find the files")
	}
}

// An explicit off is the maintainer's own opt out of the check, and it is kept.
func TestSoulseekBlockMode_RespectsContentCheckOff(t *testing.T) {
	f := soulseekFixture(t)
	f.setMode(t, ebookCheckKey, "off")
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported with the check off", dl.Status, dl.ErrorMessage)
	}
}

// An EPUB that does not open is refused for Soulseek. The same file from a
// torrent is not judged here.
func TestSoulseekBlockMode_RefusesAnUnreadableEpub(t *testing.T) {
	f := soulseekFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "Mary Mary.epub"), []byte("this is not a zip archive"+strings.Repeat(" ", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked || !strings.Contains(dl.ErrorMessage, "Mary Mary.epub") ||
		!strings.Contains(dl.ErrorMessage, "could not be opened") {
		t.Fatalf("status %q message %q", dl.Status, dl.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 || !f.blocklisted(t) {
		t.Errorf("files %d blocklisted %v", f.bookFileCount(t), f.blocklisted(t))
	}
}

func TestSoulseekBlockMode_RefusesTheWrongFormatForTheSlot(t *testing.T) {
	f := soulseekFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "01 Mary Mary.mp3"), []byte("ID3 audio bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked || !strings.Contains(dl.ErrorMessage, "grabbed as an ebook") {
		t.Fatalf("status %q message %q", dl.Status, dl.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("a file of the wrong format was placed")
	}
}

// The matching book imports untouched, with no content finding attached.
func TestSoulseekBlockMode_LetsTheRightBookThrough(t *testing.T) {
	f := soulseekFixture(t)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary", "James Patterson")
	f.importDownload(t)
	f.assertImportedSilently(t)
}

// PDF has no embedded metadata Bindery reads, so it has no content check: it
// passes the format rule and imports.
func TestSoulseekBlockMode_PDFHasNoContentCheck(t *testing.T) {
	f := soulseekFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "Mary Mary.pdf"), []byte("%PDF-1.4 "+strings.Repeat("x", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
}

// A person importing by hand has already chosen the file.
func TestSoulseekBlockMode_ManualImportSkipsTheGate(t *testing.T) {
	f := soulseekFixture(t)
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.scanner.tryImportInternal(withManualImport(t.Context()), f.dl, f.dir, "slskd", "batch", "", nil, nil)
	if dl, _ := f.reload(t); dl.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
}
