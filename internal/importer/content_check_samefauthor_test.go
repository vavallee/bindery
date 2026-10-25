package importer

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// addCatalogueBook puts another book of the fixture's author in the catalogue.
func (f *contentFixture) addCatalogueBook(t *testing.T, foreignID, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: f.author.ID, Title: title,
		SortTitle: strings.ToLower(title), Status: models.BookStatusWanted, Monitored: false,
		AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

// placeInSeries puts the books in one new series at the given positions and
// wires the series repo into the scanner.
func (f *contentFixture) placeInSeries(t *testing.T, books []*models.Book, positions []string) {
	t.Helper()
	repo := db.NewSeriesRepo(f.sqlDB)
	f.scanner.WithSeriesRepo(repo)
	ser := &models.Series{ForeignID: "OL-CC-S", Title: "The Series"}
	if err := repo.Create(context.Background(), ser); err != nil {
		t.Fatal(err)
	}
	for i, b := range books {
		if err := repo.LinkBook(context.Background(), ser.ID, b.ID, positions[i], true); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	hawkFileTitle   = "City of the Dead: A Maximum Ride Novel"
	hawkFileCreator = "James Patterson, Mindy McGinnis"
)

// #3461: the file is another book of the same author. The author agrees, so the
// old check (title AND author must disagree) let it through three times. Hawk is
// book 1 and City of the Dead book 2 of one series, which is what makes the
// verdict confident enough to refuse the file.
func TestEbookContentCheck_BlockRefusesAnotherBookBySameAuthor(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	other := f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
	f.placeInSeries(t, []*models.Book{f.book, other}, []string{"1", "2"})
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", hawkFileTitle, hawkFileCreator)
	f.importDownload(t)

	dl, book := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	for _, want := range []string{"City of the Dead", "Hawk", "James Patterson"} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("message %q does not name %q", dl.ErrorMessage, want)
		}
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("book status = %q, want wanted", book.Status)
	}
	if !f.blocklisted(t) {
		t.Error("the release was not blocklisted")
	}
}

// Warn mode only notes it: the file imports and History carries the note.
func TestEbookContentCheck_WarnImportsAnotherBookBySameAuthorWithANote(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	other := f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
	f.placeInSeries(t, []*models.Book{f.book, other}, []string{"1", "2"})
	f.writeEpubTitled(t, "book.epub", hawkFileTitle, hawkFileCreator)
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if f.blocklisted(t) {
		t.Error("warn mode blocklisted the release")
	}
	if _, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any); !ok {
		t.Error("bookImported event has no contentCheck note")
	}
}

// With the other book in the catalogue but nothing saying whether it is an
// edition or a distinct work, block mode downgrades to a note: the file imports.
func TestEbookContentCheck_UncertainOtherBookNotesButNeverBlocks(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", hawkFileTitle, hawkFileCreator)
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImported {
		t.Fatalf("status = %q (%s), want imported", dl.Status, dl.ErrorMessage)
	}
	if f.blocklisted(t) {
		t.Error("an uncertain finding blocklisted the release")
	}
	cc, ok := f.eventData(t, models.HistoryEventBookImported)["contentCheck"].(map[string]any)
	if !ok || cc["uncertain"] != true {
		t.Errorf("want an uncertain contentCheck note, got %v", cc)
	}
}

// Both editions of one book held as separate rows: the other edition is not
// another book, whether or not they share an id, and excluded rows count too.
func TestEbookContentCheck_BothEditionsInTheCatalogueImportSilently(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *contentFixture, other *models.Book)
	}{
		{"shared ASIN", func(t *testing.T, f *contentFixture, other *models.Book) {
			for _, b := range []*models.Book{f.book, other} {
				b.ASIN = "B000SHARED"
				if err := f.books.Update(context.Background(), b); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"same series same position", func(t *testing.T, f *contentFixture, other *models.Book) {
			f.placeInSeries(t, []*models.Book{f.book, other}, []string{"1", "1"})
		}},
		{"original title names the wanted one", func(t *testing.T, f *contentFixture, other *models.Book) {
			other.OriginalTitle = "Northern Lights"
			if err := f.books.Update(context.Background(), other); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newContentFixture(t, "Northern Lights", "Philip Pullman")
			other := f.addCatalogueBook(t, "HC-CC-B2", "The Golden Compass")
			c.setup(t, f, other)
			f.setMode(t, ebookCheckKey, "block")
			f.writeEpubTitled(t, "book.epub", "The Golden Compass", "Philip Pullman")
			f.importDownload(t)
			f.assertImportedSilently(t)
		})
	}
}

// Without the other book in the catalogue the file might be an alternate or
// translated title of the wanted one, so nothing is said.
func TestEbookContentCheck_UnknownOtherTitleByTheSameAuthorImports(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	f.addCatalogueBook(t, "OL-CC-B2", "Zoo")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", hawkFileTitle, hawkFileCreator)
	f.importDownload(t)
	f.assertImportedSilently(t)
}

// A translated title by the same author, with the author's other books known,
// is still not another book.
func TestEbookContentCheck_TranslatedTitleBySameAuthorImports(t *testing.T) {
	f := newContentFixture(t, "War and Peace", "Leo Tolstoy")
	f.addCatalogueBook(t, "OL-CC-B2", "Anna Karenina")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "book.epub", "Voyna i mir", "Leo Tolstoy")
	f.importDownload(t)
	f.assertImportedSilently(t)
}

func slots(pos ...string) []seriesSlot {
	out := make([]seriesSlot, len(pos))
	for i, p := range pos {
		out[i] = seriesSlot{seriesID: 1, position: p}
	}
	return out
}

func TestEbookIsAnotherBookOfTheAuthor(t *testing.T) {
	patterson := []string{"James Patterson"}
	hawk := catalogueBook{id: 1, title: "Hawk", ids: []string{"OL1"}, slots: slots("1")}
	cotd := catalogueBook{id: 2, title: "City of the Dead", ids: []string{"OL2"}, slots: slots("2")}
	cotdNoSeries := catalogueBook{id: 2, title: "City of the Dead", ids: []string{"OL2"}}
	angel := catalogueBook{id: 3, title: "Maximum Ride: The Angel Experiment", ids: []string{"OL3"}, slots: slots("3")}
	pm := catalogueBook{id: 4, title: "The President Is Missing", ids: []string{"OL4"}}
	cat := []catalogueBook{hawk, cotd, angel, pm}
	plain := func(id int64, title string, others ...string) catalogueBook {
		return catalogueBook{id: id, title: title, ids: append([]string{"x" + title}, others...)}
	}

	rows := []struct {
		name, fileTitle, fileBy string
		wanted                  catalogueBook
		names                   []string
		catalogue               []catalogueBook
		want                    otherWork
	}{
		{"the #3461 report", hawkFileTitle, hawkFileCreator, hawk, patterson, cat, otherWorkConfident},
		{"second credit is the wanted author", "City of the Dead", "Mindy McGinnis, James Patterson", hawk, patterson, cat, otherWorkConfident},
		{"last first credit", "City of the Dead", "Patterson, James", hawk, patterson, cat, otherWorkConfident},
		{"series prefix on the file", "Maximum Ride: The Angel Experiment", "James Patterson", hawk, patterson, cat, otherWorkConfident},
		{"no series data: likely only", "City of the Dead", "James Patterson", hawk, patterson, []catalogueBook{hawk, cotdNoSeries}, otherWorkLikely},
		{"no signals at all: likely only", "City of the Dead", "James Patterson", plain(1, "Hawk"), patterson, []catalogueBook{plain(1, "Hawk"), plain(2, "City of the Dead")}, otherWorkLikely},

		{"other book in a different series: likely only", "City of the Dead", "James Patterson", hawk, patterson,
			[]catalogueBook{hawk, {id: 2, title: "City of the Dead", slots: []seriesSlot{{seriesID: 2, position: "2"}}}}, otherWorkLikely},

		// Provider formatting and bad data must not read as a different position.
		{"1 against 1.0", "The Golden Compass", "Philip Pullman", catalogueBook{id: 1, title: "Northern Lights", slots: slots("1")}, []string{"Philip Pullman"},
			[]catalogueBook{{id: 2, title: "The Golden Compass", slots: slots("1.0")}}, otherWorkNo},
		{"1 against 01", "The Golden Compass", "Philip Pullman", catalogueBook{id: 1, title: "Northern Lights", slots: slots("1")}, []string{"Philip Pullman"},
			[]catalogueBook{{id: 2, title: "The Golden Compass", slots: slots("01")}}, otherWorkNo},
		{"omnibus range position is unknown", "City of the Dead", "James Patterson", catalogueBook{id: 1, title: "Hawk", slots: slots("1")}, patterson,
			[]catalogueBook{{id: 2, title: "City of the Dead", slots: slots("1-3")}}, otherWorkLikely},
		{"non numeric position is unknown", "City of the Dead", "James Patterson", catalogueBook{id: 1, title: "Hawk", slots: slots("one")}, patterson,
			[]catalogueBook{{id: 2, title: "City of the Dead", slots: slots("2")}}, otherWorkLikely},
		{"decimal position still differs", "City of the Dead", "James Patterson", catalogueBook{id: 1, title: "Hawk", slots: slots("1")}, patterson,
			[]catalogueBook{{id: 2, title: "City of the Dead", slots: slots("1.5")}}, otherWorkConfident},

		// Both editions present as separate rows, both directions.
		{"both editions, no shared id", "The Golden Compass", "Philip Pullman", plain(1, "Northern Lights"), []string{"Philip Pullman"},
			[]catalogueBook{plain(1, "Northern Lights"), plain(2, "The Golden Compass"), plain(3, "The Subtle Knife")}, otherWorkLikely},
		{"both editions, reverse, no shared id", "Northern Lights", "Philip Pullman", plain(2, "The Golden Compass"), []string{"Philip Pullman"},
			[]catalogueBook{plain(1, "Northern Lights"), plain(2, "The Golden Compass"), plain(3, "The Subtle Knife")}, otherWorkLikely},
		{"both editions, shared id", "The Golden Compass", "Philip Pullman", plain(1, "Northern Lights", "shared"), []string{"Philip Pullman"},
			[]catalogueBook{plain(1, "Northern Lights", "shared"), plain(2, "The Golden Compass", "shared")}, otherWorkNo},
		{"both editions, shared id, reverse", "Northern Lights", "Philip Pullman", plain(2, "The Golden Compass", "shared"), []string{"Philip Pullman"},
			[]catalogueBook{plain(1, "Northern Lights", "shared"), plain(2, "The Golden Compass", "shared")}, otherWorkNo},
		{"excluded duplicate with a shared id", "The Golden Compass", "Philip Pullman", plain(1, "Northern Lights", "shared"), []string{"Philip Pullman"},
			[]catalogueBook{plain(1, "Northern Lights", "shared"), plain(5, "The Golden Compass", "shared")}, otherWorkNo},
		{"same series same position", "The Golden Compass", "Philip Pullman",
			catalogueBook{id: 1, title: "Northern Lights", slots: slots("1")}, []string{"Philip Pullman"},
			[]catalogueBook{{id: 2, title: "The Golden Compass", slots: slots("1")}}, otherWorkNo},
		{"original title names the wanted one", "The Golden Compass", "Philip Pullman", plain(1, "Northern Lights"), []string{"Philip Pullman"},
			[]catalogueBook{{id: 2, title: "The Golden Compass", originalTitle: "Northern Lights"}}, otherWorkNo},
		{"wanted original title is the file", "The Golden Compass", "Philip Pullman",
			catalogueBook{id: 1, title: "Northern Lights", originalTitle: "The Golden Compass"}, []string{"Philip Pullman"},
			[]catalogueBook{{id: 2, title: "The Golden Compass"}}, otherWorkNo},

		// Must stay silent.
		{"the wanted book itself, with a subtitle", "Hawk: A Maximum Ride Novel", "James Patterson", hawk, patterson, cat, otherWorkNo},
		{"other book not in the catalogue", "City of the Dead", "James Patterson", hawk, patterson, []catalogueBook{hawk}, otherWorkNo},
		{"empty catalogue", "City of the Dead", "James Patterson", hawk, patterson, nil, otherWorkNo},
		{"no creator", "City of the Dead", "", hawk, patterson, cat, otherWorkNo},
		{"placeholder creator", "City of the Dead", "Unknown", hawk, patterson, cat, otherWorkNo},
		{"no title", "", "James Patterson", hawk, patterson, cat, otherWorkNo},
		{"different author is the old rule's job", "City of the Dead", "Somebody Else", hawk, patterson, cat, otherWorkNo},
		{"translated title of the wanted book", "Voyna i mir", "Leo Tolstoy", plain(1, "War and Peace"), []string{"Leo Tolstoy"},
			[]catalogueBook{plain(1, "War and Peace"), plain(2, "Anna Karenina")}, otherWorkNo},
		{"same book with an or title", "The Hobbit", "J.R.R. Tolkien", plain(1, "The Hobbit, or There and Back Again"), []string{"J.R.R. Tolkien"},
			[]catalogueBook{plain(1, "The Hobbit, or There and Back Again"), plain(2, "The Silmarillion")}, otherWorkNo},
		{"leading article", "Name of the Wind", "Patrick Rothfuss", plain(1, "The Name of the Wind"), []string{"Patrick Rothfuss"},
			[]catalogueBook{plain(1, "The Name of the Wind"), plain(2, "The Wise Man's Fear")}, otherWorkNo},
		{"numbered volume, same work", "Defiance of the Fall 01", "TheFirstDefier", plain(1, "Defiance of the Fall 17"), []string{"TheFirstDefier"},
			[]catalogueBook{plain(1, "Defiance of the Fall 17"), plain(2, "Defiance of the Fall 01")}, otherWorkNo},
		{"catalogue omnibus holding the wanted book", "Hawk and City of the Dead", "James Patterson", hawk, patterson,
			[]catalogueBook{hawk, plain(9, "Hawk and City of the Dead Omnibus")}, otherWorkNo},
		{"non Latin edition", "Преступление и наказание", "Фёдор Достоевский", plain(1, "Crime and Punishment"), []string{"Fyodor Dostoevsky"},
			[]catalogueBook{plain(1, "Crime and Punishment"), plain(2, "The Idiot")}, otherWorkNo},
	}
	for _, r := range rows {
		if got := ebookIsAnotherBookOfTheAuthor(r.wanted, r.names, r.fileTitle, r.fileBy, r.catalogue); got != r.want {
			t.Errorf("%s: got %v, want %v", r.name, got, r.want)
		}
	}
}

// A release is uncertain only when every mismatched file is. One definite and one
// uncertain EPUB is a definite release, and block mode refuses it.
func TestEbookContentCheck_MixedDefiniteAndUncertainFilesStillBlock(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
	f.setMode(t, ebookCheckKey, "block")
	f.writeEpubTitled(t, "a.epub", hawkFileTitle, hawkFileCreator)
	f.writeEpubTitled(t, "b.epub", "Moby Dick", "Herman Melville")
	f.importDownload(t)

	dl, _ := f.reload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
}
