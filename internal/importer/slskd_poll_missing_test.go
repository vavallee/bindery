package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/slskd"
	"github.com/vavallee/bindery/internal/downloader/slskd/slskdtest"
	"github.com/vavallee/bindery/internal/models"
)

// slskd soft deletes finished transfers ("clear completed", retention), so a
// batch can vanish from its list while the files sit in the download's folder.
// That is a finished download, not a failed one.
func TestSlskdPoll_BatchGoneButFilesPresentImports(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:gonefiles"})
	f.writeBook(t, dl)
	f.dl = dl
	f.fake.Batches = map[string]*slskdtest.Batch{}
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 1 {
		t.Errorf("book files = %d", f.bookFileCount(t))
	}
}

// With nothing importable in the folder the download fails, and whatever is in
// the folder is left alone.
func TestSlskdPoll_BatchGoneAndNoFilesFailsWithoutDeleting(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:goneempty"})
	if err := os.MkdirAll(f.folder(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(f.folder(dl), "notes.txt")
	if err := os.WriteFile(keep, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fake.Batches = map[string]*slskdtest.Batch{}
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "no longer has this download") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a file was deleted for a batch that merely went missing: %v", err)
	}
}

// A symlink or a linked directory inside the folder is never followed.
func TestSlskdPoll_SymlinksInsideTheFolderAreNotImported(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:links"})
	outside := t.TempDir()
	big := strings.Repeat("x", 6000)
	if err := os.WriteFile(filepath.Join(outside, "secret.epub"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "dir", "inner.epub"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.folder(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.epub"), filepath.Join(f.folder(dl), "link.epub")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(f.folder(dl), "linkdir")); err != nil {
		t.Fatal(err)
	}
	f.fake.SetAll(f.batchID(dl), "Completed, Succeeded", 6000)
	f.dl = dl
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status == models.StateImported || f.bookFileCount(t) != 0 {
		t.Fatalf("a symlinked file was imported: status %q files %d", got.Status, f.bookFileCount(t))
	}
}

// bindery/ itself replaced by a link to somewhere else is refused.
func TestSlskdPoll_SymlinkedParentIsRefused(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:parentlink"})
	outside := t.TempDir()
	real := filepath.Join(outside, filepath.Base(f.folder(dl)))
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	f.dir = real
	f.writeEpubTitled(t, "Mary Mary.epub", "Mary, Mary", "James Patterson")
	if err := os.RemoveAll(filepath.Join(f.root, "bindery")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "bindery")); err != nil {
		t.Skip("symlinks unavailable")
	}
	f.fake.SetAll(f.batchID(dl), "Completed, Succeeded", 6000)
	f.dl = dl
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateImportFailed || !strings.Contains(got.ErrorMessage, "resolves outside") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("imported through a symlinked parent")
	}
}

// The batch vanished while only some of its files finished: the rest sit in
// slskd's incomplete folder. Importing the finished ones would be a half
// download. Fail before the count check: it imported the one file.
func TestSlskdPoll_BatchGoneWithFewerFilesThanRequestedFailsWithoutImport(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:gonepartial", files: []slskd.BatchFile{
		{Filename: `@@p\Mary Mary\Mary Mary.epub`, Size: 6000},
		{Filename: `@@p\Mary Mary\cover.jpg`, Size: 500},
	}})
	f.writeBook(t, dl)
	f.dl = dl
	f.fake.Batches = map[string]*slskdtest.Batch{}
	f.poll()
	got := f.get(t, dl.ID)
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "1 of its 2 files") || !strings.Contains(got.ErrorMessage, "left in") {
		t.Fatalf("status %q message %q", got.Status, got.ErrorMessage)
	}
	if f.bookFileCount(t) != 0 {
		t.Error("a half download was imported")
	}
	if _, err := os.Stat(filepath.Join(f.folder(dl), "Mary Mary.epub")); err != nil {
		t.Errorf("a finished file was deleted: %v", err)
	}
	// A later poll must not pick the failed row up again.
	f.poll()
	if f.bookFileCount(t) != 0 {
		t.Error("a later poll imported the partial folder")
	}
}

// Every requested file is in the folder (any type counts): import.
func TestSlskdPoll_BatchGoneWithAllFilesImports(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:goneall", files: []slskd.BatchFile{
		{Filename: `@@p\Mary Mary\Mary Mary.epub`, Size: 6000},
		{Filename: `@@p\Mary Mary\cover.jpg`, Size: 500},
	}})
	f.writeBook(t, dl)
	if err := os.WriteFile(filepath.Join(f.folder(dl), "cover.jpg"), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.dl = dl
	f.fake.Batches = map[string]*slskdtest.Batch{}
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
}

// The grab address cannot be read, so the expected count is unknown (0). That
// only happens for a row whose address was lost; it keeps the old rule of
// importing any book file found.
func TestSlskdPoll_BatchGoneWithUnknownFileCountKeepsImporting(t *testing.T) {
	f := newSlskdPollFixture(t)
	dl := f.start(t, slskdGrabOpts{guid: "slskd:goneunknown", files: []slskd.BatchFile{
		{Filename: `@@p\Mary Mary\Mary Mary.epub`, Size: 6000},
		{Filename: `@@p\Mary Mary\cover.jpg`, Size: 500},
	}})
	if _, err := f.sqlDB.Exec(`UPDATE downloads SET nzb_url = '' WHERE id = ?`, dl.ID); err != nil {
		t.Fatal(err)
	}
	f.writeBook(t, dl)
	f.dl = dl
	f.fake.Batches = map[string]*slskdtest.Batch{}
	f.poll()
	if got := f.get(t, dl.ID); got.Status != models.StateImported {
		t.Fatalf("status %q (%s), want imported", got.Status, got.ErrorMessage)
	}
}
