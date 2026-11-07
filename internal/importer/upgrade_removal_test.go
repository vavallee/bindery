package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The removal gate needs positive evidence. Each of these EPUBs is imported (it
// does not contradict the book) but none affirmatively says it is the book, so
// the MOBI must stay even though the profile removes replaced files.
func TestEbookUpgrade_RemovalNeedsPositiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, title, creator string
	}{
		{"no title and no creator", "", ""},
		{"different title and no creator", "Totally Different Book", ""},
		{"other book by the right author", "Some Unlisted Novel", "James Patterson"},
		{"right title and another creator", "Mary, Mary", "Somebody Else"},
		{"right title and no creator", "Mary, Mary", ""},
		{"right creator and no title", "", "James Patterson"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpgradeFixture(t, true, true)
			f.writeEpubTitled(t, "book.epub", tc.title, tc.creator)
			f.importDownload(t)
			if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
				t.Errorf("the MOBI was removed on a file that did not positively match the book")
			}
		})
	}
}

// A Calibre book's file is Calibre's to manage.
func TestEbookUpgrade_NeverRemovesInsideCalibre(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	if _, err := f.sqlDB.Exec(`UPDATE books SET calibre_id = 42 WHERE id = ?`, f.book.ID); err != nil {
		t.Fatal(err)
	}
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	if !f.epubTracked(t) {
		t.Fatal("the EPUB was not imported")
	}
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("the MOBI of a Calibre book was removed")
	}
}

// No delete wired means nothing is ever deleted.
func TestEbookUpgrade_NoRemoverMeansNoRemoval(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.scanner.WithReplacedFileRemover(nil)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("the MOBI was removed with no guarded delete wired")
	}
}

// A path the guard refuses (outside the roots, or another book's) keeps both
// the file and its row.
func TestEbookUpgrade_RefusedByTheGuardKeepsTheFileAndRow(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.scanner.WithReplacedFileRemover(func(context.Context, int64, string) (bool, error) { return true, nil })
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	if !f.mobiOnDisk(t) || !f.mobiTracked(t) {
		t.Error("a path the guard refused was removed")
	}
}

// A verified file that is not an upgrade cannot vouch for an unread one.
func TestRemoveReplaced_OnlyVerifiedFormatsVouch(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	up := &ebookUpgrade{profile: &models.QualityProfile{
		UpgradeAllowed: true, RemoveReplaced: true, Cutoff: "epub",
		Items: []models.QualityItem{{Quality: "epub", Allowed: true}, {Quality: "mobi", Allowed: true}},
	}, existing: []string{f.oldMobi}}
	newEpub := filepath.Join(f.library, "James Patterson", "Mary, Mary", "Mary, Mary.epub")
	if err := os.WriteFile(newEpub, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only a MOBI was verified; the EPUB that landed was never read.
	if got := f.scanner.removeReplaced(context.Background(), f.book, up, []string{newEpub}, []string{"mobi"}); len(got) != 0 {
		t.Fatalf("removed %v on the strength of a verified lower ranked file", got)
	}
	if !f.mobiOnDisk(t) {
		t.Error("the MOBI was removed")
	}
	if got := f.scanner.removeReplaced(context.Background(), f.book, up, []string{newEpub}, []string{"epub"}); len(got) != 1 {
		t.Fatalf("a verified EPUB did not remove the MOBI: %v", got)
	}
}
