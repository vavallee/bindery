package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

func expanseCatalog() *metadata.SeriesCatalog {
	author := &models.Author{ForeignID: "hc:james-s-a-corey", Name: "James S. A. Corey", SortName: "Corey, James S. A.", MetadataProvider: "hardcover"}
	released := time.Date(2011, 6, 15, 0, 0, 0, 0, time.UTC)
	entry := func(foreignID, providerID, title, position string, dated bool) metadata.SeriesCatalogBook {
		b := models.Book{ForeignID: foreignID, Title: title, SortTitle: title, MetadataProvider: "hardcover", Author: author}
		if dated {
			b.ReleaseDate = &released
		}
		return metadata.SeriesCatalogBook{ForeignID: foreignID, ProviderID: providerID, Title: title, Position: position, Book: b}
	}
	books := []metadata.SeriesCatalogBook{
		entry("hc:leviathan-wakes", "1001", "Leviathan Wakes", "1", true),
		entry("hc:calibans-war", "1002", "Caliban's War", "2", false),
		entry("hc:abaddons-gate", "1003", "Abaddon's Gate", "3", true),
		entry("hc:the-expanse-books-1-3", "1004", "The Expanse Books 1-3", "0", true),
	}
	return &metadata.SeriesCatalog{
		ForeignID: "hc-series:expanse", ProviderID: "777", Title: "The Expanse",
		AuthorName: "James S. A. Corey", BookCount: len(books), Books: books,
	}
}

// profileSeriesFixture is enhancedSeriesFixture with the metadata profile
// repo wired, and the seeded default profile set to skip part books and
// undated works.
func profileSeriesFixture(t *testing.T, catalog *metadata.SeriesCatalog, searcher BookSearcher) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	profiles := db.NewMetadataProfileRepo(database)
	if err := settingsRepo.Set(ctx, SettingHardcoverAPIToken, "hc-secret"); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, SettingHardcoverEnhancedSeriesEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	p, err := profiles.GetByID(ctx, models.DefaultMetadataProfileID)
	if err != nil || p == nil {
		t.Fatalf("default profile: %v %v", p, err)
	}
	p.SkipPartBooks = true
	p.SkipMissingDate = true
	// Hardcover series entries carry no language. Under "unknown fails" a
	// fill must still create them; the language is resolved later.
	p.UnknownLanguageBehavior = models.UnknownLanguageFail
	if err := profiles.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	provider := &stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog}}
	h := NewSeriesHandler(seriesRepo, bookRepo, authorRepo, metadata.NewAggregator(provider).WithAudnexClient(nil), searcher).
		WithHardcoverFeatureSettings(settingsRepo, true).
		WithMetadataProfiles(profiles)
	return h, seriesRepo, authorRepo, bookRepo
}

// TestSeriesFillAppliesMetadataProfileFilters is the #2208 series fill
// regression test. ensureHardcoverCatalogBook consulted no metadata profile,
// so "add all" created the part books and undated works the author's profile
// screens out, and the re-queue loop re-monitored stored rows the profile
// rejects. An explicit add of one named book stays exempt, as a single work
// add from an author page is (#1612).
func TestSeriesFillAppliesMetadataProfileFilters(t *testing.T) {
	catalog := expanseCatalog()
	h, seriesRepo, authorRepo, bookRepo := profileSeriesFixture(t, catalog, newMockBookSearcher())
	ctx := context.Background()
	series := linkedSeries(t, seriesRepo, catalog)

	// A box set row an earlier fill created, which the user has since
	// unmonitored. Fill must not put it back on Wanted.
	author := &models.Author{ForeignID: "hc:james-s-a-corey", Name: "James S. A. Corey", SortName: "Corey, James S. A.", MetadataProvider: "hardcover"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	released := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	boxSet := &models.Book{
		ForeignID: "hc:the-expanse-books-4-6", AuthorID: author.ID, Title: "The Expanse Books 4-6", SortTitle: "The Expanse Books 4-6",
		Status: models.BookStatusWanted, Monitored: false, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "hardcover", ReleaseDate: &released,
	}
	if err := bookRepo.Create(ctx, boxSet); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, boxSet.ID, "4.5", true); err != nil {
		t.Fatal(err)
	}
	// An undated novella the user added and monitored by hand. It is already
	// being sought, so fill queues it as before and does not report it as
	// skipped on every run.
	handPicked := &models.Book{
		ForeignID: "hc:the-churn", AuthorID: author.ID, Title: "The Churn", SortTitle: "The Churn",
		Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, handPicked); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, series.ID, handPicked.ID, "3.5", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	for _, fid := range []string{"hc:leviathan-wakes", "hc:abaddons-gate"} {
		got, err := bookRepo.GetByForeignID(ctx, fid)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Errorf("%s passes every filter but was not created", fid)
		}
	}
	for _, fid := range []string{"hc:calibans-war", "hc:the-expanse-books-1-3"} {
		got, err := bookRepo.GetByForeignID(ctx, fid)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Errorf("fill created %q although the metadata profile filters it out", got.Title)
		}
	}
	stored, err := bookRepo.GetByID(ctx, boxSet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Monitored {
		t.Error("fill re-monitored a stored box set the metadata profile filters out")
	}
	if body["queued"] != 3 || body["skippedByProfile"] != 3 {
		t.Errorf("response = %+v, want queued 3 (two created, the hand picked novella) and skippedByProfile 3", body)
	}

	// The per row add names one book on purpose and is not screened.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill",
		bytes.NewBufferString(`{"foreignBookId":"hc:calibans-war","providerId":"1002","position":"2"}`))
	h.Fill(rec, withURLParam(req, "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit add: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := bookRepo.GetByForeignID(ctx, "hc:calibans-war"); err != nil || got == nil {
		t.Errorf("explicit add of a filtered book did not create it: %v %v", got, err)
	}
}

// TestSeriesFillWithoutProfileRepoFiltersNothing pins the fallback: a handler
// with no profile repo wired behaves as before rather than guessing.
func TestSeriesFillWithoutProfileRepoFiltersNothing(t *testing.T) {
	catalog := expanseCatalog()
	h, seriesRepo, _, bookRepo, _ := enhancedSeriesFixture(t, catalog, newMockBookSearcher())
	series := linkedSeries(t, seriesRepo, catalog)

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(context.Background(), "hc:calibans-war")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("an undated book was filtered with no profile repo wired")
	}
}
