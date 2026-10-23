package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// #3368: the explicit link route refuses a Hardcover id another series holds,
// and the auto route leaves it alone instead of taking it over.

func TestSeriesPutHardcoverLinkRejectsIDHeldByAnotherSeries(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	ctx := context.Background()
	owner := &models.Series{ForeignID: "manual:series:a", Title: "Stormlight"}
	other := &models.Series{ForeignID: "manual:series:b", Title: "Stormlight copy"}
	for _, s := range []*models.Series{owner, other} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: owner.ID, HardcoverSeriesID: "hc-series:42", Confidence: 1}); err != nil {
		t.Fatal(err)
	}

	put := func(id int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"foreignId":"hc-series:42"}`)
		h.PutHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/hardcover-link", body), "id", strconv.FormatInt(id, 10)))
		return rec
	}
	rec := put(other.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Error    string `json:"error"`
		SeriesID int64  `json:"seriesId"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.SeriesID != owner.ID || got.Error == "" {
		t.Fatalf("body = %+v, want seriesId %d", got, owner.ID)
	}
	if link, _ := repo.GetHardcoverLink(ctx, other.ID); link != nil {
		t.Fatalf("rejected series got a link: %+v", link)
	}
	// Relinking the owner to its own id is not a conflict.
	if rec := put(owner.ID); rec.Code != http.StatusOK {
		t.Fatalf("owner relink status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSeriesAutoLinkHardcoverSkipsIDHeldByAnotherSeries(t *testing.T) {
	catalog := stormlightCatalog()
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{
			ForeignID: catalog.ForeignID, ProviderID: catalog.ProviderID, Title: catalog.Title,
			AuthorName: catalog.AuthorName, BookCount: catalog.BookCount,
		}},
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}
	h, repo, authorRepo, bookRepo := seriesFixtureWithProvider(t, provider, nil)
	ctx := context.Background()
	owner := &models.Series{ForeignID: "manual:series:a", Title: "Mine"}
	other := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	for _, s := range []*models.Series{owner, other} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: owner.ID, HardcoverSeriesID: catalog.ForeignID, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "hc:the-way-of-kings", AuthorID: author.ID, Title: "The Way of Kings", SortTitle: "The Way of Kings",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover"}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LinkBookIfMissing(ctx, other.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.AutoLinkHardcover(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/hardcover-link/auto", nil), "id", strconv.FormatInt(other.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp seriesHardcoverAutoResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Linked || resp.ExistingSeriesID != owner.ID {
		t.Fatalf("response = %+v, want not linked and existingSeriesId %d", resp, owner.ID)
	}
	if link, _ := repo.GetHardcoverLink(ctx, other.ID); link != nil {
		t.Fatalf("auto link took the id over: %+v", link)
	}
}
