package importer

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// addCatalogueBook puts another book of the fixture's author in the catalogue.
func (f *contentFixture) addCatalogueBook(t *testing.T, foreignID, title string) {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: f.author.ID, Title: title,
		SortTitle: strings.ToLower(title), Status: models.BookStatusWanted, Monitored: false,
		AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

const (
	hawkFileTitle   = "City of the Dead: A Maximum Ride Novel"
	hawkFileCreator = "James Patterson, Mindy McGinnis"
)

// #3461: the file is another book of the same author. The author agrees, so the
// old check (title AND author must disagree) let it through three times.
func TestEbookContentCheck_BlockRefusesAnotherBookBySameAuthor(t *testing.T) {
	f := newContentFixture(t, "Hawk", "James Patterson")
	f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
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
	f.addCatalogueBook(t, "OL-CC-B2", "City of the Dead")
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

func TestEbookIsAnotherBookOfTheAuthor(t *testing.T) {
	patterson := []string{"James Patterson"}
	cat := []string{"Hawk", "City of the Dead", "Maximum Ride: The Angel Experiment", "The President Is Missing", "Mary, Mary: Alex Cross, Book 11"}
	rows := []struct {
		name, book, fileTitle, fileBy string
		names, catalogue              []string
		want                          bool
	}{
		{"the #3461 report", "Hawk", "City of the Dead: A Maximum Ride Novel", "James Patterson, Mindy McGinnis", patterson, cat, true},
		{"second credit is the wanted author", "Hawk", "City of the Dead", "Mindy McGinnis, James Patterson", patterson, cat, true},
		{"last first credit", "Hawk", "City of the Dead", "Patterson, James", patterson, cat, true},
		{"series prefix on the file", "Hawk", "Maximum Ride: The Angel Experiment", "James Patterson", patterson, cat, true},

		// Must stay silent.
		{"the wanted book itself, with a subtitle", "Hawk", "Hawk: A Maximum Ride Novel", "James Patterson", patterson, cat, false},
		{"other book not in the catalogue", "Hawk", "City of the Dead", "James Patterson", patterson, []string{"Hawk"}, false},
		{"empty catalogue", "Hawk", "City of the Dead", "James Patterson", patterson, nil, false},
		{"no creator", "Hawk", "City of the Dead", "", patterson, cat, false},
		{"placeholder creator", "Hawk", "City of the Dead", "Unknown", patterson, cat, false},
		{"no title", "Hawk", "", "James Patterson", patterson, cat, false},
		{"different author is the old rule's job", "Hawk", "City of the Dead", "Somebody Else", patterson, cat, false},
		{"translated title of the wanted book", "War and Peace", "Voyna i mir", "Leo Tolstoy", []string{"Leo Tolstoy"}, []string{"War and Peace", "Anna Karenina"}, false},
		{"alternate title the catalogue does not hold", "Northern Lights", "The Golden Compass", "Philip Pullman", []string{"Philip Pullman"}, []string{"Northern Lights", "The Subtle Knife"}, false},
		{"same book with an or title", "The Hobbit, or There and Back Again", "The Hobbit", "J.R.R. Tolkien", []string{"J.R.R. Tolkien"}, []string{"The Hobbit", "The Silmarillion"}, false},
		{"series prefix on the book", "The Wheel of Time, Book 1: The Eye of the World", "The Eye of the World", "Robert Jordan", []string{"Robert Jordan"}, []string{"The Great Hunt"}, false},
		{"leading article", "The Name of the Wind", "Name of the Wind", "Patrick Rothfuss", []string{"Patrick Rothfuss"}, []string{"The Wise Man's Fear"}, false},
		{"numbered volume, same work", "Defiance of the Fall 17", "Defiance of the Fall 01", "TheFirstDefier", []string{"TheFirstDefier"}, []string{"Defiance of the Fall 01", "Defiance of the Fall 17"}, false},
		{"catalogue lists an omnibus holding the wanted book", "Hawk", "Hawk and City of the Dead", "James Patterson", patterson, []string{"Hawk and City of the Dead Omnibus"}, false},
		{"non Latin edition", "Crime and Punishment", "Преступление и наказание", "Фёдор Достоевский", []string{"Fyodor Dostoevsky"}, []string{"The Idiot"}, false},
	}
	for _, r := range rows {
		if got := ebookIsAnotherBookOfTheAuthor(r.book, r.names, r.fileTitle, r.fileBy, r.catalogue); got != r.want {
			t.Errorf("%s: got %v, want %v", r.name, got, r.want)
		}
	}
}
