package api

import (
	"context"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// refreshFieldsFixture builds an author with one OpenLibrary book and runs a
// refresh whose catalogue record is incoming (#3105).
type refreshFieldsFixture struct {
	t        *testing.T
	sqlExec  func(string) error
	books    *db.BookRepo
	run      func(models.Book)
	existing *models.Book
	ctx      context.Context
}

func newRefreshFieldsFixture(t *testing.T, seed models.Book) *refreshFieldsFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL3105A", Name: "Refresh Fields", SortName: "Fields, Refresh",
		MetadataProvider: "openlibrary", Monitored: true,
		MonitorMode: models.AuthorMonitorModeAll, MonitorNewItems: models.AuthorMonitorNewItemsAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	seed.AuthorID = author.ID
	seed.ForeignID = "OL3105W"
	seed.Title = "Fields Title"
	seed.SortTitle = "fields title"
	seed.MetadataProvider = "openlibrary"
	if seed.Status == "" {
		seed.Status = models.BookStatusWanted
	}
	if seed.Genres == nil {
		seed.Genres = []string{}
	}
	if err := bookRepo.Create(ctx, &seed); err != nil {
		t.Fatal(err)
	}
	f := &refreshFieldsFixture{t: t, books: bookRepo, existing: &seed, ctx: ctx}
	f.sqlExec = func(q string) error { _, err := database.ExecContext(ctx, q); return err }
	f.run = func(incoming models.Book) {
		incoming.ForeignID = "OL3105W"
		incoming.Title = "Fields Title"
		incoming.SortTitle = "fields title"
		incoming.MetadataProvider = "openlibrary"
		incoming.Status = models.BookStatusWanted
		incoming.Genres = []string{}
		stub := &stubMetaProvider{name: "openlibrary", works: []models.Book{incoming}}
		h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, &searcherSpy{})
		h.RefreshAuthorBooks(author, false, "")
	}
	return f
}

func (f *refreshFieldsFixture) stored() *models.Book {
	f.t.Helper()
	b, err := f.books.GetByID(f.ctx, f.existing.ID)
	if err != nil || b == nil {
		f.t.Fatalf("reload book: %v", err)
	}
	return b
}

func year(y int) *time.Time {
	d := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	return &d
}

func TestRefreshAuthorBooks_UpdatesProviderFieldsFromRecord(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999), Language: "fre"})
	f.run(models.Book{ReleaseDate: year(2002), Language: "eng", Description: "A fresh blurb."})
	got := f.stored()
	if got.ReleaseDate == nil || got.ReleaseDate.Year() != 2002 {
		t.Errorf("release date corrected upstream did not reach the book: %v", got.ReleaseDate)
	}
	if got.Language != "eng" {
		t.Errorf("language = %q, want eng", got.Language)
	}
	if got.Description != "A fresh blurb." {
		t.Errorf("empty description was not filled: %q", got.Description)
	}
}

func TestRefreshAuthorBooks_LockedFieldsAreNotUpdated(t *testing.T) {
	d := time.Date(2000, 5, 6, 0, 0, 0, 0, time.UTC)
	f := newRefreshFieldsFixture(t, models.Book{
		ReleaseDate: &d, Language: "fre", Description: "mine",
		LockedFields: []string{models.BookFieldReleaseDate, models.BookFieldLanguage, models.BookFieldDescription},
	})
	f.run(models.Book{ReleaseDate: year(2002), Language: "eng", Description: "theirs"})
	got := f.stored()
	if got.ReleaseDate == nil || !got.ReleaseDate.Equal(d) || got.Language != "fre" || got.Description != "mine" {
		t.Errorf("locked fields changed: date=%v lang=%q desc=%q", got.ReleaseDate, got.Language, got.Description)
	}
}

func TestRefreshAuthorBooks_NeverClearsFields(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999), Language: "fre", Description: "kept"})
	// A record with nothing to say, for example because the search enrichment failed.
	f.run(models.Book{})
	got := f.stored()
	if got.ReleaseDate == nil || got.ReleaseDate.Year() != 1999 || got.Language != "fre" || got.Description != "kept" {
		t.Errorf("an empty record cleared fields: date=%v lang=%q desc=%q", got.ReleaseDate, got.Language, got.Description)
	}
}

func TestRefreshAuthorBooks_UnknownFieldIsNotAdopted(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999), Language: "fre"})
	f.run(models.Book{ReleaseDate: year(2002), Language: "eng",
		UnknownFields: []string{models.BookFieldReleaseDate, models.BookFieldLanguage}})
	got := f.stored()
	if got.ReleaseDate.Year() != 1999 || got.Language != "fre" {
		t.Errorf("fields the record could not determine were adopted: date=%v lang=%q", got.ReleaseDate, got.Language)
	}
}

func TestRefreshAuthorBooks_DescriptionIsFillOnly(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{Description: "imported text"})
	f.run(models.Book{Description: "provider text"})
	if got := f.stored().Description; got != "imported text" {
		t.Errorf("an existing unlocked description was replaced: %q", got)
	}
}

func TestRefreshAuthorBooks_YearOnlyDateDoesNotReplacePreciseDate(t *testing.T) {
	d := time.Date(2002, 3, 4, 0, 0, 0, 0, time.UTC)
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: &d})
	f.run(models.Book{ReleaseDate: year(2002)})
	if got := f.stored().ReleaseDate; got == nil || !got.Equal(d) {
		t.Errorf("a precise date was replaced by the year only record: %v", got)
	}
}

func TestRefreshAuthorBooks_OtherProviderRowIsNotUpdated(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999), Language: "fre"})
	if err := f.sqlExec(`UPDATE books SET metadata_provider = 'audiobookshelf'`); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002), Language: "eng"})
	got := f.stored()
	if got.ReleaseDate.Year() != 1999 || got.Language != "fre" {
		t.Errorf("a row from another provider was updated: date=%v lang=%q", got.ReleaseDate, got.Language)
	}
}

func TestRefreshAuthorBooks_ExcludedBookIsNotUpdated(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	if err := f.sqlExec(`UPDATE books SET excluded = 1`); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002)})
	all, err := f.books.ListIncludingExcluded(f.ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	if all[0].ReleaseDate.Year() != 1999 {
		t.Errorf("an excluded book was updated: %v", all[0].ReleaseDate)
	}
}

// A write that loses its guard (an edit landed between the re-read and the
// write) must change nothing; the trigger stands in for that edit.
func TestRefreshAuthorBooks_LostGuardKeepsTheEdit(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	if err := f.sqlExec(`CREATE TRIGGER test_lose_guard BEFORE UPDATE OF release_date ON books
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002)})
	if got := f.stored().ReleaseDate; got.Year() != 1999 {
		t.Errorf("a lost guard still wrote: %v", got)
	}
	if err := f.sqlExec(`DROP TRIGGER test_lose_guard`); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002)})
	if got := f.stored().ReleaseDate; got.Year() != 2002 {
		t.Errorf("the next sync did not make the change: %v", got)
	}
}
