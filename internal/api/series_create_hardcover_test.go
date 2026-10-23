package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// #1706: POST /series with a "hardcover" object creates the series from a
// Hardcover search result and links it in one step.

const createHCBody = `{"hardcover":{"foreignId":"hc-series:42","providerId":"42","slug":"the-stormlight-archive","title":"The Stormlight Archive","authorName":"Brandon Sanderson","bookCount":4}}`

func createSeriesReq(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/series", bytes.NewBufferString(body))
}

func countSeriesRows(t *testing.T, repo *db.SeriesRepo) int {
	t.Helper()
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

func stormlightProvider() *stubSeriesProvider {
	c := stormlightCatalog()
	return &stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{c.ForeignID: c}}
}

func TestSeriesCreateFromHardcoverCreatesAndLinks(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Series
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.Title != "The Stormlight Archive" {
		t.Fatalf("series = %+v, want title defaulted from hardcover", got)
	}
	if got.HardcoverLink == nil || got.HardcoverLink.HardcoverSeriesID != "hc-series:42" || got.HardcoverLink.SeriesID != got.ID {
		t.Fatalf("response link = %+v", got.HardcoverLink)
	}
	stored, err := repo.GetHardcoverLink(context.Background(), got.ID)
	if err != nil || stored == nil || stored.HardcoverSeriesID != "hc-series:42" || stored.HardcoverSlug != "the-stormlight-archive" {
		t.Fatalf("stored link = %+v, err %v", stored, err)
	}
	if n := countSeriesRows(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
}

func TestSeriesCreateFromHardcoverKeepsExplicitTitle(t *testing.T) {
	h, _, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"title":"  Stormlight  ","hardcover":{"foreignId":"hc-series:42"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Series
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.Title != "Stormlight" {
		t.Fatalf("title = %q, want the explicit one", got.Title)
	}
}

func TestSeriesCreateFromHardcoverProviderFailureLeavesNothing(t *testing.T) {
	p := stormlightProvider()
	p.catalogErr = errors.New("hardcover is down")
	h, repo, _, _ := seriesFixtureWithProvider(t, p, nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

func TestSeriesCreateFromHardcoverRollsBackWhenLinkCannotBeStored(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE TRIGGER refuse_link BEFORE INSERT ON series_hardcover_links
		BEGIN SELECT RAISE(ABORT, 'link refused'); END`); err != nil {
		t.Fatal(err)
	}
	repo := db.NewSeriesRepo(database)
	h := NewSeriesHandler(repo, db.NewBookRepo(database), db.NewAuthorRepo(database),
		metadata.NewAggregator(stormlightProvider()).WithAudnexClient(nil), &mockBookSearcher{})
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want the new series rolled back", n)
	}
}

func TestSeriesCreateFromHardcoverDuplicateReturnsExisting(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	ctx := context.Background()
	existing := &models.Series{ForeignID: "manual:series:mine", Title: "My Stormlight"}
	if err := repo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: existing.ID, HardcoverSeriesID: "hc-series:42", Confidence: 1, LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Error    string `json:"error"`
		SeriesID int64  `json:"seriesId"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.SeriesID != existing.ID || got.Error == "" {
		t.Fatalf("body = %+v, want seriesId %d", got, existing.ID)
	}
	if n := countSeriesRows(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
}

// A series Bindery made from author sync carries the Hardcover id as its
// foreign id and may have no link row yet; it still counts as already there.
func TestSeriesCreateFromHardcoverDuplicateByForeignID(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	existing := &models.Series{ForeignID: "hc-series:42", Title: "Stormlight"}
	if err := repo.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
}

func TestSeriesCreateFromHardcoverRefusedWhenFeatureDisabled(t *testing.T) {
	h, repo, _, _, _ := seriesFixtureWithProviderAndSettings(t, stormlightProvider(), nil, false)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "enhanced hardcover api disabled" {
		t.Fatalf("status = %d %s, want 404 enhanced hardcover api disabled", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

func TestSeriesCreateFromHardcoverNeedsForeignID(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"title":"X","hardcover":{"foreignId":"  "}}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

// Without a hardcover object the route is the manual title path, unchanged:
// no link, no provider call, and it works with enhanced Hardcover off.
func TestSeriesCreateWithoutHardcoverUnchanged(t *testing.T) {
	p := stormlightProvider()
	h, repo, _, _, _ := seriesFixtureWithProviderAndSettings(t, p, nil, false)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"title":"Manual"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Series
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if got.Title != "Manual" || got.HardcoverLink != nil {
		t.Fatalf("series = %+v", got)
	}
	if link, _ := repo.GetHardcoverLink(context.Background(), got.ID); link != nil {
		t.Fatalf("manual create stored a link: %+v", link)
	}
	if p.catalogCalls != 0 {
		t.Fatalf("catalog calls = %d, want 0", p.catalogCalls)
	}
}
