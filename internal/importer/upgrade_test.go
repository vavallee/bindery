package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// upgradeFixture is a book that already has a MOBI, an author whose profile
// ranks EPUB above MOBI and aims for EPUB, and a finished download that is
// about to be imported (#2420).
type upgradeFixture struct {
	*contentFixture
	oldMobi string
}

func newUpgradeFixture(t *testing.T, upgradeAllowed, removeReplaced bool) *upgradeFixture {
	t.Helper()
	f := newContentFixture(t, "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	ctx := context.Background()

	profiles := db.NewQualityProfileRepo(f.sqlDB)
	profile := &models.QualityProfile{
		Name:           "epub first",
		UpgradeAllowed: upgradeAllowed,
		RemoveReplaced: removeReplaced,
		Cutoff:         "epub",
		Items: []models.QualityItem{
			{Quality: "epub", Allowed: true},
			{Quality: "mobi", Allowed: true},
		},
	}
	if err := profiles.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	f.author.QualityProfileID = &profile.ID
	if err := f.authors.Update(ctx, f.author); err != nil {
		t.Fatal(err)
	}
	f.scanner.WithFormatEnforcement(profiles, f.blocklist)
	// Stands in for the api package's guarded delete, which the importer
	// cannot import: library roots only.
	library := f.library
	f.scanner.WithReplacedFileRemover(func(_ context.Context, _ int64, path string) (bool, error) {
		if !strings.HasPrefix(path, library+string(filepath.Separator)) {
			return true, nil
		}
		return false, os.Remove(path)
	})

	// The book already holds a MOBI, tracked and on disk.
	oldMobi := filepath.Join(f.library, "James Patterson", "Mary, Mary", "Mary, Mary.mobi")
	if err := os.MkdirAll(filepath.Dir(oldMobi), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldMobi, []byte("old mobi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, f.book.ID, models.MediaTypeEbook, oldMobi); err != nil {
		t.Fatal(err)
	}
	return &upgradeFixture{contentFixture: f, oldMobi: oldMobi}
}

func (f *upgradeFixture) mobiOnDisk(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(f.oldMobi)
	return err == nil
}

func (f *upgradeFixture) mobiTracked(t *testing.T) bool {
	t.Helper()
	files, err := f.books.ListFiles(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, bf := range files {
		if bf.Path == f.oldMobi {
			return true
		}
	}
	return false
}

func (f *upgradeFixture) epubTracked(t *testing.T) bool {
	t.Helper()
	files, err := f.books.ListFiles(context.Background(), f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, bf := range files {
		if filepath.Ext(bf.Path) == ".epub" {
			if _, err := os.Stat(bf.Path); err == nil {
				return true
			}
		}
	}
	return false
}

// The default: the EPUB lands beside the MOBI and the MOBI is left alone.
func TestEbookUpgrade_KeepsTheOldFileByDefault(t *testing.T) {
	f := newUpgradeFixture(t, true, false)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if !f.epubTracked(t) {
		t.Error("the EPUB was not imported")
	}
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("the MOBI was removed although the profile keeps replaced files")
	}
	data := f.eventData(t, models.HistoryEventBookImported)
	if data["upgrade"] != true {
		t.Errorf("history event is not marked as an upgrade: %v", data)
	}
	if f.notif.lookup("upgrade") == nil {
		t.Error("no upgrade notification was published")
	}
	if f.notif.lookup("bookImported") != nil {
		t.Error("an upgrade also published bookImported")
	}
}

// With the profile set to remove, a verified upgrade deletes the MOBI and its
// row.
func TestEbookUpgrade_RemovesTheOldFileWhenTheProfileSaysSo(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if !f.epubTracked(t) {
		t.Fatal("the EPUB was not imported")
	}
	if f.mobiOnDisk(t) || f.mobiTracked(t) {
		t.Error("the MOBI survived a remove-replaced upgrade")
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("book status = %q, want imported", book.Status)
	}
}

// kennethp1015's requirement: a wrong book must never replace a good file.
// The default content check mode is warn, which would import it; for an
// upgrade it refuses, and the MOBI is untouched even with remove on.
func TestEbookUpgrade_AWrongBookLeavesTheOldFileUntouched(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.writeEpubTitled(t, "book.epub", "Mary and Mr Eliot: A Sort of Love Story", "Mary Trevelyan")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("the MOBI was touched by a refused upgrade")
	}
	if f.epubTracked(t) {
		t.Error("the wrong book was imported")
	}
	if !f.blocklisted(t) {
		t.Error("the refused release was not blocklisted")
	}
}

// A file the check could not read verifies nothing, so even a remove profile
// keeps the old file.
func TestEbookUpgrade_UnverifiedKeepsTheOldFile(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.setMode(t, ebookCheckKey, "off")
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if !f.epubTracked(t) {
		t.Error("the EPUB was not imported")
	}
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("the MOBI was removed although the new file was never checked")
	}
}

// Upgrades off is the old behaviour: nothing is removed and a mismatch is only
// noted in warn mode.
func TestEbookUpgrade_OffChangesNothing(t *testing.T) {
	f := newUpgradeFixture(t, false, true)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("an import without upgrades on removed the MOBI")
	}
	if f.notif.lookup("upgrade") != nil {
		t.Error("an ordinary import published an upgrade")
	}
}
