package api

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// runWithGenres is f.run for a record that carries genres (run blanks them).
func (f *refreshFieldsFixture) runWithGenres(incoming models.Book) {
	incoming.ForeignID = "OL3105W"
	incoming.Title = "Fields Title"
	incoming.SortTitle = "fields title"
	incoming.MetadataProvider = "openlibrary"
	incoming.Status = models.BookStatusWanted
	f.runAll(incoming)
}

func TestRefreshAuthorBooks_FillsEmptyLanguageAndGenres(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{})
	f.runWithGenres(models.Book{Language: "eng", Genres: []string{"Fantasy"}})
	got := f.stored()
	if got.Language != "eng" {
		t.Errorf("empty language was not filled: %q", got.Language)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "Fantasy" {
		t.Errorf("empty genres were not filled: %v", got.Genres)
	}
}

func TestRefreshAuthorBooks_LockedLanguageAndGenresStayEmpty(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{
		LockedFields: []string{models.BookFieldLanguage, models.BookFieldGenres},
	})
	f.runWithGenres(models.Book{Language: "eng", Genres: []string{"Fantasy"}})
	got := f.stored()
	if got.Language != "" || len(got.Genres) != 0 {
		t.Errorf("locked fields were filled: lang=%q genres=%v", got.Language, got.Genres)
	}
}

func TestRefreshAuthorBooks_ExistingGenresAreKept(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{Genres: []string{"Mine"}})
	f.runWithGenres(models.Book{Genres: []string{"Fantasy"}})
	if got := f.stored().Genres; len(got) != 1 || got[0] != "Mine" {
		t.Errorf("existing genres were changed: %v", got)
	}
}

func TestRefreshAuthorBooks_OtherProviderRowGetsNoLanguageOrGenres(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{})
	if err := f.sqlExec(`UPDATE books SET metadata_provider = 'audiobookshelf'`); err != nil {
		t.Fatal(err)
	}
	f.runWithGenres(models.Book{Language: "eng", Genres: []string{"Fantasy"}})
	if got := f.stored(); got.Language != "" || len(got.Genres) != 0 {
		t.Errorf("a row from another provider was filled: lang=%q genres=%v", got.Language, got.Genres)
	}
}

func TestRefreshAuthorBooks_UnknownLanguageIsNotFilled(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{})
	f.runWithGenres(models.Book{Language: "eng", UnknownFields: []string{models.BookFieldLanguage}})
	if got := f.stored().Language; got != "" {
		t.Errorf("a language the record could not determine was adopted: %q", got)
	}
}
