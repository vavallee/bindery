package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Parallel identical requests must make one series, not one per request.
func TestSeriesCreateFromHardcoverParallelMakesOneSeries(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			h.Create(rec, createSeriesReq(createHCBody))
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("codes = %v, want one 201 and %d 409", codes, n-1)
	}
	if rows := countSeriesRows(t, repo); rows != 1 {
		t.Fatalf("series rows = %d, want 1", rows)
	}
}

func TestSeriesCreateFromHardcoverUnknownIDIsNotFound(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"title":"Junk","hardcover":{"foreignId":"hc-series:999","title":"Junk"}}`))
	if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "hardcover series not found" {
		t.Fatalf("status = %d %s, want 404 hardcover series not found", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

func TestSeriesCreateFromHardcoverRejectsMalformedID(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	for _, id := range []string{"42", "hc-series:", "hc-series:abc", "hc-series:42 or 1=1", "hc:42", "manual:series:1"} {
		rec := httptest.NewRecorder()
		h.Create(rec, createSeriesReq(`{"hardcover":{"foreignId":"`+id+`"}}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d, want 400: %s", id, rec.Code, rec.Body.String())
		}
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

func TestSeriesCreateFromHardcoverRejectsOversizedStrings(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	long := strings.Repeat("x", 600)
	for _, field := range []string{"title", "authorName", "slug", "providerId"} {
		rec := httptest.NewRecorder()
		h.Create(rec, createSeriesReq(`{"hardcover":{"foreignId":"hc-series:42","`+field+`":"`+long+`"}}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", field, rec.Code)
		}
	}
	if n := countSeriesRows(t, repo); n != 0 {
		t.Fatalf("series rows = %d, want 0", n)
	}
}

// A confidence above 1 is clamped, and a negative book count never reaches the
// stored link.
func TestSeriesCreateFromHardcoverClampsNumbers(t *testing.T) {
	c := stormlightCatalog()
	p := &stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{c.ForeignID: c}}
	h, repo, _, _ := seriesFixtureWithProvider(t, p, nil)
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"hardcover":{"foreignId":"hc-series:42","bookCount":-5,"confidence":7}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Series
	_ = json.NewDecoder(rec.Body).Decode(&got)
	stored, _ := repo.GetHardcoverLink(context.Background(), got.ID)
	if stored == nil || stored.Confidence != 1 {
		t.Fatalf("stored link = %+v, want confidence clamped to 1", stored)
	}
	if stored.HardcoverBookCount < 0 {
		t.Fatalf("book count = %d, want >= 0", stored.HardcoverBookCount)
	}
}

// A duplicate costs no Hardcover call and still answers while the provider is down.
func TestSeriesCreateFromHardcoverDuplicateAnswersWithProviderDown(t *testing.T) {
	p := stormlightProvider()
	p.catalogErr = errors.New("hardcover is down")
	h, repo, _, _ := seriesFixtureWithProvider(t, p, nil)
	ctx := context.Background()
	existing := &models.Series{ForeignID: "manual:series:mine", Title: "Mine"}
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
	if p.catalogCalls != 0 {
		t.Fatalf("catalog calls = %d, want 0", p.catalogCalls)
	}
}

// The catalogue can resolve the client id to another one; a series linked
// under the id the client sent is still a duplicate.
func TestSeriesCreateFromHardcoverDuplicateByClientID(t *testing.T) {
	c := stormlightCatalog()
	p := &stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{"hc-series:99": c}}
	h, repo, _, _ := seriesFixtureWithProvider(t, p, nil)
	ctx := context.Background()
	existing := &models.Series{ForeignID: "manual:series:mine", Title: "Mine"}
	if err := repo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: existing.ID, HardcoverSeriesID: "hc-series:99", Confidence: 1, LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(`{"hardcover":{"foreignId":"hc-series:99"}}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if n := countSeriesRows(t, repo); n != 1 {
		t.Fatalf("series rows = %d, want 1", n)
	}
}

// A series whose foreign id is a bare "42" belongs to some other provider.
func TestSeriesCreateFromHardcoverBareIDIsNotADuplicate(t *testing.T) {
	h, repo, _, _ := seriesFixtureWithProvider(t, stormlightProvider(), nil)
	other := &models.Series{ForeignID: "42", Title: "Unrelated"}
	if err := repo.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByHardcoverSeriesID(context.Background(), "42")
	if err != nil || got != nil {
		t.Fatalf("GetByHardcoverSeriesID(42) = %+v, %v; want nil", got, err)
	}
	rec := httptest.NewRecorder()
	h.Create(rec, createSeriesReq(createHCBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}
