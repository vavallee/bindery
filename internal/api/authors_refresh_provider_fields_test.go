package api

import (
	"context"
	"database/sql"
	"fmt"
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
	runAll   func(...models.Book)
	h        *AuthorHandler
	author   *models.Author
	database *sql.DB
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
	f := &refreshFieldsFixture{t: t, books: bookRepo, existing: &seed, ctx: ctx, author: author, database: database}
	f.sqlExec = func(q string) error { _, err := database.ExecContext(ctx, q); return err }
	f.runAll = func(works ...models.Book) {
		stub := &stubMetaProvider{name: "openlibrary", works: works}
		f.h = NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(stub), nil, profileRepo, &searcherSpy{})
		f.h.RefreshAuthorBooks(author, false, "")
	}
	f.run = func(incoming models.Book) {
		incoming.ForeignID = "OL3105W"
		incoming.Title = "Fields Title"
		incoming.SortTitle = "fields title"
		incoming.MetadataProvider = "openlibrary"
		incoming.Status = models.BookStatusWanted
		incoming.Genres = []string{}
		f.runAll(incoming)
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
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	f.run(models.Book{ReleaseDate: year(2002), Description: "A fresh blurb."})
	got := f.stored()
	if got.ReleaseDate == nil || got.ReleaseDate.Year() != 2002 {
		t.Errorf("release date corrected upstream did not reach the book: %v", got.ReleaseDate)
	}
	if got.Description != "A fresh blurb." {
		t.Errorf("empty description was not filled: %q", got.Description)
	}
}

func TestRefreshAuthorBooks_LockedFieldsAreNotUpdated(t *testing.T) {
	d := time.Date(2000, 5, 6, 0, 0, 0, 0, time.UTC)
	f := newRefreshFieldsFixture(t, models.Book{
		ReleaseDate: &d, Description: "",
		LockedFields: []string{models.BookFieldReleaseDate, models.BookFieldDescription},
	})
	f.run(models.Book{ReleaseDate: year(2002), Description: "theirs"})
	got := f.stored()
	if got.ReleaseDate == nil || !got.ReleaseDate.Equal(d) || got.Description != "" {
		t.Errorf("locked fields changed: date=%v desc=%q", got.ReleaseDate, got.Description)
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
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	f.run(models.Book{ReleaseDate: year(2002), Description: "x",
		UnknownFields: []string{models.BookFieldReleaseDate, models.BookFieldDescription}})
	got := f.stored()
	if got.ReleaseDate.Year() != 1999 || got.Description != "" {
		t.Errorf("fields the record could not determine were adopted: date=%v desc=%q", got.ReleaseDate, got.Description)
	}
}

func TestRefreshAuthorBooks_LanguageIsNotAdopted(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{Language: "fre"})
	f.run(models.Book{Language: "eng"})
	if got := f.stored().Language; got != "fre" {
		t.Errorf("language was adopted from a catalogue record: %q", got)
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
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	if err := f.sqlExec(`UPDATE books SET metadata_provider = 'audiobookshelf'`); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002)})
	if got := f.stored(); got.ReleaseDate.Year() != 1999 {
		t.Errorf("a row from another provider was updated: date=%v", got.ReleaseDate)
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

// A row resolved through a secondary id (here the Hardcover one) is not the
// record's own work, so its date is left alone.
func TestRefreshAuthorBooks_SecondaryIdMatchIsNotUpdated(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	if err := f.books.UpsertBookIdentifier(f.ctx, f.existing.ID, "OL-OTHER-W"); err != nil {
		t.Fatal(err)
	}
	f.runAll(models.Book{
		ForeignID: "OL-OTHER-W", Title: "Fields Title", SortTitle: "fields title", ReleaseDate: year(2011),
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
	})
	if got := f.stored(); got.ReleaseDate.Year() != 1999 {
		t.Errorf("a row matched through another work's id took that work's date: %v", got.ReleaseDate)
	}
}

// Two works with one title: the row belongs to the first, and neither the title
// match nor the later id match may swap the other's date onto it, run after run.
func TestRefreshAuthorBooks_SameTitleWorksLeaveTheRowStable(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1965)})
	mk := func(id string, y int) models.Book {
		return models.Book{ForeignID: id, Title: "Fields Title", SortTitle: "fields title", ReleaseDate: year(y),
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}
	}
	var updatedAt string
	for i := 0; i < 4; i++ {
		f.runAll(mk("OL3105W", 1965), mk("OL3105X", 2011))
		got := f.stored()
		if got.ReleaseDate.Year() != 1965 {
			t.Fatalf("run %d: the row's date flipped to %v", i, got.ReleaseDate)
		}
		if i == 1 {
			updatedAt = got.UpdatedAtRaw
		}
		if i > 1 && got.UpdatedAtRaw != updatedAt {
			t.Fatalf("run %d: updated_at moved (%q to %q) although nothing changed", i, updatedAt, got.UpdatedAtRaw)
		}
	}
}

func TestRefreshAuthorBooks_ABSConflictFieldIsNotUpdated(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	if err := f.sqlExec(fmt.Sprintf(`INSERT INTO abs_metadata_conflicts
		(source_id, library_id, item_id, entity_type, local_id, field_name, abs_value, upstream_value,
		 applied_source, preferred_source, resolution_status, created_at, updated_at)
		VALUES ('s','l','i','book',%d,'release_date','1999-01-01','2002-01-01','abs','abs','resolved',datetime('now'),datetime('now'))`, f.existing.ID)); err != nil {
		t.Fatal(err)
	}
	f.run(models.Book{ReleaseDate: year(2002), Description: "filled"})
	got := f.stored()
	if got.ReleaseDate.Year() != 1999 {
		t.Errorf("a field with an ABS conflict choice was overwritten: %v", got.ReleaseDate)
	}
	if got.Description != "filled" {
		t.Errorf("a field without a conflict should still be filled: %q", got.Description)
	}
}

// An edit that lands between the re-read and the write must win. The seam makes
// the edit; a write that ignored the guard would overwrite it.
func TestRefreshAuthorBooks_ConcurrentEditWinsAndNextSyncRetries(t *testing.T) {
	f := newRefreshFieldsFixture(t, models.Book{ReleaseDate: year(1999)})
	edited := false
	hook := func() {
		if edited {
			return
		}
		edited = true
		cur, err := f.books.GetByID(f.ctx, f.existing.ID)
		if err != nil || cur == nil {
			t.Errorf("load for edit: %v", err)
			return
		}
		cur.Monitored = false
		cur.ImageURL = "/covers/user-choice.jpg"
		if err := f.books.Update(f.ctx, cur); err != nil {
			t.Errorf("edit: %v", err)
		}
	}
	// First run, with the edit landing mid-write.
	stub := &stubMetaProvider{name: "openlibrary", works: []models.Book{{
		ForeignID: "OL3105W", Title: "Fields Title", SortTitle: "fields title", ReleaseDate: year(2002),
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"}}}
	h := NewAuthorHandler(db.NewAuthorRepo(f.database), nil, f.books, nil, metadata.NewAggregator(stub), nil,
		db.NewMetadataProfileRepo(f.database), &searcherSpy{})
	h.afterProviderFieldsRead = hook
	h.RefreshAuthorBooks(f.author, false, "")
	got := f.stored()
	if got.Monitored || got.ImageURL != "/covers/user-choice.jpg" {
		t.Fatalf("the concurrent edit was overwritten: monitored=%v image=%q", got.Monitored, got.ImageURL)
	}
	if got.ReleaseDate.Year() != 1999 {
		t.Fatalf("a lost guard still wrote the date: %v", got.ReleaseDate)
	}
	// The next sync makes the change.
	h.RefreshAuthorBooks(f.author, false, "")
	if got := f.stored(); got.ReleaseDate.Year() != 2002 || got.Monitored {
		t.Errorf("next sync: date=%v monitored=%v", got.ReleaseDate, got.Monitored)
	}
}
