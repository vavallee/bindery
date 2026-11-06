package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func probeProfile(t *testing.T, f *upgradeFixture, cutoff string, items ...string) {
	t.Helper()
	repo := db.NewQualityProfileRepo(f.sqlDB)
	p, err := repo.GetByID(context.Background(), *f.author.QualityProfileID)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	p.Cutoff = cutoff
	p.Items = nil
	for _, i := range items {
		p.Items = append(p.Items, models.QualityItem{Quality: i, Allowed: true})
	}
	if err := repo.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func TestEbookUpgradeGate_Titles(t *testing.T) {
	for _, tc := range []struct {
		name, title, creator string
		wantRemoved          bool
	}{
		{"exact", "Mary, Mary: Alex Cross, Book 11", "James Patterson", true},
		{"short title", "Mary, Mary", "James Patterson", false},
		{"series prefix", "Alex Cross: Mary, Mary", "James Patterson", false}, // a leading series name looks like any other prefix, so it only loses the delete
		{"first word only", "Mary", "James Patterson", false},
		{"box set", "Mary, Mary: The Complete Collection Box Set", "James Patterson", false},
		{"other series book", "Alex Cross, Book 11", "James Patterson", false},
		{"sequel number", "Mary, Mary: Alex Cross, Book 12", "James Patterson", false},
		{"last first", "Mary, Mary: Alex Cross, Book 11", "Patterson, James", true},
		{"initial", "Mary, Mary: Alex Cross, Book 11", "J. Patterson", true},
		{"surname only", "Mary, Mary", "Patterson", false},
		{"with coauthor", "Mary, Mary: Alex Cross, Book 11", "James Patterson, Maxine Paetro", true},
		{"similar name", "Mary, Mary", "James Pattersen", false},
		{"other James", "Mary, Mary", "James Smith", false},
		{"author as title", "James Patterson", "James Patterson", false},
		{"unknown creator", "Mary, Mary", "Unknown", false},
		{"creator Various", "Mary, Mary", "Various", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpgradeFixture(t, true, true)
			f.writeEpubTitled(t, "book.epub", tc.title, tc.creator)
			f.importDownload(t)
			removed := !f.mobiOnDisk(t)
			t.Logf("title=%q creator=%q removed=%v epubTracked=%v", tc.title, tc.creator, removed, f.epubTracked(t))
			if removed != tc.wantRemoved {
				t.Errorf("removed=%v want %v", removed, tc.wantRemoved)
			}
			if removed && f.mobiTracked(t) {
				t.Errorf("row left behind")
			}
		})
	}
}

func TestEbookUpgradeGate_LowerThanCutoff(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	probeProfile(t, f, "epub", "epub", "azw3", "mobi")
	f.writeEpubTitled(t, "book.azw3", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	t.Logf("removed=%v tracked files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed on sub-cutoff upgrade")
	}
}

func TestEbookUpgradeGate_StrayPDFHigherRank(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	probeProfile(t, f, "pdf", "pdf", "epub", "mobi")
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	if err := os.WriteFile(filepath.Join(f.dir, "book.pdf"), []byte("%PDF-1.4 stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	t.Logf("removed=%v files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed though the only verified file (epub) is below the pdf cutoff")
	}
}

func TestEbookUpgradeGate_StrayPDFWrongEpub(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	probeProfile(t, f, "epub", "pdf", "epub", "mobi")
	f.writeEpubTitled(t, "book.epub", "Some Unlisted Novel", "")
	if err := os.WriteFile(filepath.Join(f.dir, "book.pdf"), []byte("%PDF-1.4 stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	t.Logf("removed=%v files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed on unverified epub + pdf")
	}
}

func TestEbookUpgradeGate_PDFOnly(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	probeProfile(t, f, "pdf", "pdf", "epub", "mobi")
	if err := os.WriteFile(filepath.Join(f.dir, "book.pdf"), []byte("%PDF-1.4 stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	t.Logf("removed=%v files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed on unread pdf")
	}
}

func TestEbookUpgradeGate_Truncated(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	p := filepath.Join(f.dir, "book.epub")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	t.Logf("removed=%v files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed on truncated epub")
	}
}

func TestEbookUpgradeGate_Garbage(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	if err := os.WriteFile(filepath.Join(f.dir, "book.epub"), []byte("not a zip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.importDownload(t)
	t.Logf("removed=%v files=%d", !f.mobiOnDisk(t), f.bookFileCount(t))
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed on garbage epub")
	}
}

func TestEbookUpgradeGate_ContentCheckOff(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.setMode(t, settingImportEbookContentCheck, "off")
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	if !f.mobiOnDisk(t) {
		t.Error("mobi removed with content check off")
	}
}

func TestEbookUpgradeGate_TwoEpubsOneWrong(t *testing.T) {
	f := newUpgradeFixture(t, true, true)
	f.writeEpubTitled(t, "a.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.writeEpubTitled(t, "b.epub", "Totally Different Book", "Somebody Else")
	f.importDownload(t)
	dl, _ := f.reload(t)
	t.Logf("status=%s removed=%v files=%d", dl.Status, !f.mobiOnDisk(t), f.bookFileCount(t))
}

// A title that merely contains the book's title is another book. Only the
// exact title deletes. A subtitle that is not a series number ("Deluxe
// Edition") is treated as a different title too, so it keeps the old file and
// loses only the delete.
func TestEbookUpgradeGate_SequelContainsTitle(t *testing.T) {
	for _, tc := range []struct {
		fileTitle   string
		wantRemoved bool
	}{
		{"Dune", true},
		{"The Dune", false},
		{"Dune Messiah", false},
		{"Children of Dune", false},
		{"Dune: The Graphic Novel", false},
		{"Dune Box Set Books 1 to 6", false},
		{"Dune: Deluxe Edition", false},
		{"Dune Complete Collection", false},
		{"Dune Omnibus", false},
	} {
		ft := tc.fileTitle
		t.Run(ft, func(t *testing.T) {
			f := newUpgradeFixture(t, true, true)
			f.book.Title = "Dune"
			f.book.OriginalTitle = ""
			if err := f.books.Update(context.Background(), f.book); err != nil {
				t.Fatal(err)
			}
			f.writeEpubTitled(t, "book.epub", ft, "James Patterson")
			f.importDownload(t)
			if removed := !f.mobiOnDisk(t); removed != tc.wantRemoved {
				t.Errorf("file title %q: old mobi removed=%v, want %v", ft, removed, tc.wantRemoved)
			}
		})
	}
}

func TestEbookUpgradeGate_UpgradeOff(t *testing.T) {
	f := newUpgradeFixture(t, false, true)
	f.writeEpubTitled(t, "book.epub", "Mary, Mary: Alex Cross, Book 11", "James Patterson")
	f.importDownload(t)
	if !f.mobiOnDisk(t) {
		t.Error("removed with upgrade_allowed off")
	}
}
