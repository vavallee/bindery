package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// A provider that reports a raw code ("nob" from the National Library) must
// not undo the startup backfill that stored "nor". These cover the API sites
// that copy a provider's language onto a stored book (#3086).

func TestRebind_StoresNormalizedLanguage(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Norsk Bok", SortTitle: "Norsk Bok", Language: "nob", Genres: []string{},
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Language != "nor" {
		t.Errorf("rebind stored language %q, want nor", got.Language)
	}
}

// The ASIN metadata map copies the target's language through
// preserveBookStateForMetadataMap, and its caller then saves the book.
func TestMetadataMap_StoresNormalizedLanguage(t *testing.T) {
	h, books, _, _, _, book, ctx := rebindFixture(t)
	_ = h
	loaded, _ := books.GetByID(ctx, book.ID)
	preserveBookStateForMetadataMap(loaded, &models.Book{Title: "Norsk", Language: "nob", Genres: []string{}})
	if err := books.Update(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Language != "nor" {
		t.Errorf("metadata map stored language %q, want nor", got.Language)
	}
}

// Author sync upgrades a calibre stub from the provider's work and copies its
// language across.
func TestAuthorSync_StoresNormalizedLanguage(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	ctx := context.Background()

	profile, err := profileRepo.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || profile == nil {
		t.Fatalf("default profile: %v", err)
	}
	profile.AllowedLanguages = "nor"
	if err := profileRepo.Update(ctx, profile); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{
		ForeignID: "OL-LANG-AUTHOR", Name: "Lang Author", SortName: "Author, Lang",
		MetadataProvider: "openlibrary", Monitored: true,
		MonitorMode: models.AuthorMonitorModeAll, MonitorNewItems: models.AuthorMonitorNewItemsAll,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	stub := &models.Book{
		ForeignID: "calibre:book:61", AuthorID: author.ID, Title: "Lang Title",
		SortTitle: "lang title", Status: models.BookStatusWanted, Monitored: true,
		Genres: []string{}, MetadataProvider: "calibre",
	}
	if err := bookRepo.Create(ctx, stub); err != nil {
		t.Fatal(err)
	}
	provider := &stubMetaProvider{name: "openlibrary", works: []models.Book{{
		ForeignID: "OL-LANG-W", Title: "Lang Title", SortTitle: "lang title", Language: "nob",
		RatingsCount: 50, AverageRating: 4.2, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
	}}}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil,
		metadata.NewAggregator(provider), nil, profileRepo, &searcherSpy{})
	h.RefreshAuthorBooks(author, false, "")

	got, err := bookRepo.GetByID(ctx, stub.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.ForeignID != "OL-LANG-W" {
		t.Fatalf("stub was not upgraded (foreign id %q), the test exercised nothing", got.ForeignID)
	}
	if got.Language != "nor" {
		t.Errorf("author sync stored language %q, want nor", got.Language)
	}
}
