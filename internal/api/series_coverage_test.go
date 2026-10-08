package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// covSrSeriesFixture wires a SeriesHandler over a fresh in-memory database and
// also hands back the *sql.DB so a test can close it to force repo errors.
// provider may be nil, in which case the handler has no metadata aggregator.
type covSrSeriesFixture struct {
	database *sql.DB
	h        *SeriesHandler
	series   *db.SeriesRepo
	authors  *db.AuthorRepo
	books    *db.BookRepo
	searcher *mockBookSearcher
}

func covSrNewSeriesFixture(t *testing.T, provider *stubSeriesProvider) *covSrSeriesFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &covSrSeriesFixture{
		database: database,
		series:   db.NewSeriesRepo(database),
		authors:  db.NewAuthorRepo(database),
		books:    db.NewBookRepo(database),
		searcher: newMockBookSearcher(),
	}
	var meta *metadata.Aggregator
	if provider != nil {
		meta = metadata.NewAggregator(provider).WithAudnexClient(nil)
	}
	f.h = NewSeriesHandler(f.series, f.books, f.authors, meta, f.searcher)
	return f
}

func (f *covSrSeriesFixture) createSeries(t *testing.T, foreignID, title string) *models.Series {
	t.Helper()
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := f.series.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *covSrSeriesFixture) link(t *testing.T, seriesID int64, hardcoverID string) {
	t.Helper()
	if err := f.series.UpsertHardcoverLink(context.Background(), &models.SeriesHardcoverLink{
		SeriesID: seriesID, HardcoverSeriesID: hardcoverID, Confidence: 1, LinkedBy: "manual",
	}); err != nil {
		t.Fatal(err)
	}
}

func covSrSeriesRequest(method, body string, params map[string]string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/v1/series", nil)
	} else {
		req = httptest.NewRequest(method, "/api/v1/series", strings.NewReader(body))
	}
	if params != nil {
		req = withURLParams(req, params)
	}
	return req
}

// TestSeriesHandlersReturn500OnRepoFailure closes the database under every
// series handler whose first repo call can fail, and checks each answers the
// generic 500 body instead of a partial result or the SQL text.
func TestSeriesHandlersReturn500OnRepoFailure(t *testing.T) {
	f := covSrNewSeriesFixture(t, nil)
	if err := f.database.Close(); err != nil {
		t.Fatal(err)
	}
	idOnly := map[string]string{"id": "1"}
	cases := []struct {
		name   string
		call   http.HandlerFunc
		target string
		body   string
		params map[string]string
	}{
		{"List", f.h.List, "/api/v1/series", "", nil},
		{"List paged", f.h.List, "/api/v1/series?limit=5", "", nil},
		{"Get", f.h.Get, "", "", idOnly},
		{"Create", f.h.Create, "", `{"title":"New Series"}`, nil},
		{"Update", f.h.Update, "", `{"title":"Renamed"}`, idOnly},
		{"Delete", f.h.Delete, "", "", idOnly},
		{"AddBook", f.h.AddBook, "", `{"bookId":5}`, idOnly},
		{"RemoveBook", f.h.RemoveBook, "", "", map[string]string{"id": "1", "bookId": "5"}},
		{"SetPrimaryBook", f.h.SetPrimaryBook, "", "", map[string]string{"id": "1", "bookId": "5"}},
		{"Monitor", f.h.Monitor, "", `{"monitored":true}`, idOnly},
		{"GetHardcoverLink", f.h.GetHardcoverLink, "", "", idOnly},
		{"AutoLinkHardcover", f.h.AutoLinkHardcover, "", "", idOnly},
		{"PutHardcoverLink", f.h.PutHardcoverLink, "", `{"foreignId":"hc-series:1"}`, idOnly},
		{"DeleteHardcoverLink", f.h.DeleteHardcoverLink, "", "", idOnly},
		{"HardcoverDiff", f.h.HardcoverDiff, "", "", idOnly},
		{"Fill", f.h.Fill, "", `{}`, idOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = "/api/v1/series/1"
			}
			var req *http.Request
			if tc.body == "" {
				req = httptest.NewRequest(http.MethodPost, target, nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, target, strings.NewReader(tc.body))
			}
			if tc.params != nil {
				req = withURLParams(req, tc.params)
			}
			rec := httptest.NewRecorder()
			tc.call(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
			if got := covSrDecodeError(t, rec); got != "internal server error" {
				t.Fatalf("error = %q, want the generic message", got)
			}
		})
	}
}

// TestSeriesFillReturns500WhenCatalogExpansionReadFails covers the enhanced
// fill path with an aggregator wired: the database closing under
// createMissingHardcoverBooks must fail the request instead of silently
// queueing nothing.
func TestSeriesFillReturns500WhenCatalogExpansionReadFails(t *testing.T) {
	f := covSrNewSeriesFixture(t, &stubSeriesProvider{})
	if err := f.database.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.Fill(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": "1"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// TestSeriesManagementValidationCoverage drives the 400/404 branches of the
// manual series management endpoints that the main suite does not reach, and
// checks the store is untouched by each refusal.
func TestSeriesManagementValidationCoverage(t *testing.T) {
	f := covSrNewSeriesFixture(t, nil)
	ctx := context.Background()
	s := f.createSeries(t, "manual:series:kept", "Kept Title")
	sid := strconv.FormatInt(s.ID, 10)

	cases := []struct {
		name     string
		call     http.HandlerFunc
		body     string
		params   map[string]string
		wantCode int
		wantErr  string
	}{
		{"create bad json", f.h.Create, `{`, nil, http.StatusBadRequest, "invalid request body"},
		{"update bad id", f.h.Update, `{"title":"x"}`, map[string]string{"id": "nope"}, http.StatusBadRequest, "invalid id"},
		{"update bad json", f.h.Update, `{`, map[string]string{"id": sid}, http.StatusBadRequest, "invalid request body"},
		{"update blank title", f.h.Update, `{"title":"   "}`, map[string]string{"id": sid}, http.StatusBadRequest, "title is required"},
		{"update missing series", f.h.Update, `{"title":"x"}`, map[string]string{"id": "9999"}, http.StatusNotFound, "series not found"},
		{"delete bad id", f.h.Delete, "", map[string]string{"id": "nope"}, http.StatusBadRequest, "invalid id"},
		{"delete missing series", f.h.Delete, "", map[string]string{"id": "9999"}, http.StatusNotFound, "series not found"},
		{"add book bad id", f.h.AddBook, `{"bookId":1}`, map[string]string{"id": "nope"}, http.StatusBadRequest, "invalid id"},
		{"add book bad json", f.h.AddBook, `{`, map[string]string{"id": sid}, http.StatusBadRequest, "invalid request body"},
		{"add book missing series", f.h.AddBook, `{"bookId":1}`, map[string]string{"id": "9999"}, http.StatusNotFound, "series not found"},
		{"add book missing book", f.h.AddBook, `{"bookId":424242}`, map[string]string{"id": sid}, http.StatusNotFound, "book not found"},
		{"remove book bad series id", f.h.RemoveBook, "", map[string]string{"id": "nope", "bookId": "1"}, http.StatusBadRequest, "invalid id"},
		{"remove book bad book id", f.h.RemoveBook, "", map[string]string{"id": sid, "bookId": "nope"}, http.StatusBadRequest, "invalid book id"},
		{"remove book missing series", f.h.RemoveBook, "", map[string]string{"id": "9999", "bookId": "1"}, http.StatusNotFound, "series not found"},
		{"set primary bad series id", f.h.SetPrimaryBook, "", map[string]string{"id": "nope", "bookId": "1"}, http.StatusBadRequest, "invalid id"},
		{"set primary bad book id", f.h.SetPrimaryBook, "", map[string]string{"id": sid, "bookId": "nope"}, http.StatusBadRequest, "invalid book id"},
		{"set primary not a member", f.h.SetPrimaryBook, "", map[string]string{"id": sid, "bookId": "424242"}, http.StatusNotFound, "book is not in this series"},
		{"monitor bad json", f.h.Monitor, `{`, map[string]string{"id": sid}, http.StatusBadRequest, "invalid request body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, covSrSeriesRequest(http.MethodPost, tc.body, tc.params))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := covSrDecodeError(t, rec); got != tc.wantErr {
				t.Fatalf("error = %q, want %q", got, tc.wantErr)
			}
		})
	}

	got, err := f.series.GetByID(ctx, s.ID)
	if err != nil || got == nil {
		t.Fatalf("series vanished: %v %v", got, err)
	}
	if got.Title != "Kept Title" || len(got.Books) != 0 || got.Monitored {
		t.Fatalf("refused requests changed the series: %+v", got)
	}
}

// TestSeriesAddBookRefusesExcludedBook pins that a book the user excluded is
// treated as absent: linking it into a series would resurrect it in the
// series view.
func TestSeriesAddBookRefusesExcludedBook(t *testing.T) {
	f := covSrNewSeriesFixture(t, nil)
	ctx := context.Background()
	s := f.createSeries(t, "manual:series:x", "X")
	author := &models.Author{ForeignID: "OL-X", Name: "X Author", SortName: "Author, X", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-XB", AuthorID: author.ID, Title: "Excluded", SortTitle: "Excluded", Status: models.BookStatusWanted, Genres: []string{}}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := f.books.SetExcluded(ctx, book.ID, true); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.h.AddBook(rec, covSrSeriesRequest(http.MethodPost, `{"bookId":`+strconv.FormatInt(book.ID, 10)+`}`, map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
	if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "book not found" {
		t.Fatalf("add excluded = %d %s, want 404 book not found", rec.Code, rec.Body.String())
	}
	ids, err := f.series.GetSeriesIDsForBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("excluded book was linked into series %v", ids)
	}
}

// TestSeriesHardcoverEndpointsRefusedWhenFeatureDisabled checks every
// Hardcover link endpoint answers 404 with the disabled reason when the
// enhanced API is off, and that the refused DELETE leaves the stored link.
func TestSeriesHardcoverEndpointsRefusedWhenFeatureDisabled(t *testing.T) {
	h, seriesRepo, _, _, _ := seriesFixtureWithProviderAndSettings(t, &stubSeriesProvider{}, nil, false)
	ctx := context.Background()
	s := &models.Series{ForeignID: "manual:series:off", Title: "Off"}
	if err := seriesRepo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{SeriesID: s.ID, HardcoverSeriesID: "hc-series:off", Confidence: 1, LinkedBy: "manual"}); err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(s.ID, 10)
	cases := []struct {
		name string
		call http.HandlerFunc
		body string
	}{
		{"auto link", h.AutoLinkHardcover, ""},
		{"put link", h.PutHardcoverLink, `{"foreignId":"hc-series:other"}`},
		{"delete link", h.DeleteHardcoverLink, ""},
		{"diff", h.HardcoverDiff, ""},
		{"fill one book", h.Fill, `{"position":"1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, covSrSeriesRequest(http.MethodPost, tc.body, map[string]string{"id": sid}))
			if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "enhanced hardcover api disabled" {
				t.Fatalf("status = %d %s, want 404 enhanced hardcover api disabled", rec.Code, rec.Body.String())
			}
		})
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, s.ID)
	if err != nil || link == nil || link.HardcoverSeriesID != "hc-series:off" {
		t.Fatalf("refused requests changed the link: %+v %v", link, err)
	}
}

func TestSeriesHardcoverLinkEndpointsRejectBadIDs(t *testing.T) {
	f := covSrNewSeriesFixture(t, &stubSeriesProvider{})
	for name, call := range map[string]http.HandlerFunc{
		"get":    f.h.GetHardcoverLink,
		"auto":   f.h.AutoLinkHardcover,
		"put":    f.h.PutHardcoverLink,
		"delete": f.h.DeleteHardcoverLink,
		"diff":   f.h.HardcoverDiff,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			call(rec, covSrSeriesRequest(http.MethodPost, `{"foreignId":"hc-series:1"}`, map[string]string{"id": "abc"}))
			if rec.Code != http.StatusBadRequest || covSrDecodeError(t, rec) != "invalid id" {
				t.Fatalf("status = %d %s, want 400 invalid id", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSeriesAutoLinkHardcoverOutcomes covers the auto-link decisions that do
// not end in a stored link, plus the author-agreement evidence path that does.
func TestSeriesAutoLinkHardcoverOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("missing series", func(t *testing.T) {
		f := covSrNewSeriesFixture(t, &stubSeriesProvider{})
		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": "9999"}))
		if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "series not found" {
			t.Fatalf("status = %d %s, want 404", rec.Code, rec.Body.String())
		}
	})

	t.Run("no aggregator", func(t *testing.T) {
		f := covSrNewSeriesFixture(t, nil)
		s := f.createSeries(t, "manual:series:none", "None")
		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d %s, want 502", rec.Code, rec.Body.String())
		}
	})

	t.Run("catalog failure while scoring", func(t *testing.T) {
		provider := &stubSeriesProvider{
			searchResults: []metadata.SeriesSearchResult{{ForeignID: "hc-series:boom", Title: "Boom"}},
			catalogErr:    errors.New("hardcover down"),
		}
		f := covSrNewSeriesFixture(t, provider)
		s := f.createSeries(t, "manual:series:boom", "Boom")
		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusBadGateway || covSrDecodeError(t, rec) != "metadata provider unavailable" {
			t.Fatalf("status = %d %s, want 502", rec.Code, rec.Body.String())
		}
		if link, _ := f.series.GetHardcoverLink(ctx, s.ID); link != nil {
			t.Fatalf("failed scoring stored a link: %+v", link)
		}
	})

	t.Run("no candidates", func(t *testing.T) {
		f := covSrNewSeriesFixture(t, &stubSeriesProvider{})
		s := f.createSeries(t, "manual:series:empty", "Nothing Matches")
		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
		}
		var resp seriesHardcoverAutoResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Linked || resp.Reason != "no candidates" || resp.Candidates == nil || len(resp.Candidates) != 0 {
			t.Fatalf("response = %+v, want unlinked with no candidates", resp)
		}
	})

	t.Run("low confidence", func(t *testing.T) {
		provider := &stubSeriesProvider{
			searchResults: []metadata.SeriesSearchResult{{ForeignID: "hc-series:far", Title: "Completely Different Saga"}},
		}
		f := covSrNewSeriesFixture(t, provider)
		s := f.createSeries(t, "manual:series:low", "Quiet Garden Mysteries")
		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		var resp seriesHardcoverAutoResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || resp.Linked || resp.Reason != "low confidence" || len(resp.Candidates) != 1 {
			t.Fatalf("status %d response = %+v, want unlinked low confidence", rec.Code, resp)
		}
		if link, _ := f.series.GetHardcoverLink(ctx, s.ID); link != nil {
			t.Fatalf("low confidence stored a link: %+v", link)
		}
	})

	t.Run("author agreement is evidence", func(t *testing.T) {
		// Same series title, no overlapping book titles, but the catalog's
		// author is the author of the local book. The handler has to look the
		// author up by id because series books carry no joined author.
		catalog := &metadata.SeriesCatalog{
			ForeignID:  "hc-series:lantern",
			ProviderID: "lantern",
			Slug:       "lantern-chronicles",
			Title:      "Lantern Chronicles",
			AuthorName: "Mara Quill",
			BookCount:  1,
			Books: []metadata.SeriesCatalogBook{{
				ForeignID: "hc:unshared-volume", Title: "An Unshared Volume", Position: "1",
				Book: models.Book{ForeignID: "hc:unshared-volume", Title: "An Unshared Volume"},
			}},
		}
		provider := &stubSeriesProvider{
			// The search hit omits the slug, title, author and count, so the
			// handler must fill them from the catalog.
			searchResults: []metadata.SeriesSearchResult{{ForeignID: catalog.ForeignID}},
			catalogs:      map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
		}
		f := covSrNewSeriesFixture(t, provider)
		s := f.createSeries(t, "manual:series:lantern", "Lantern Chronicles")
		author := &models.Author{ForeignID: "OL-MQ", Name: "Mara Quill", SortName: "Quill, Mara", MetadataProvider: "openlibrary"}
		if err := f.authors.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
		for i, title := range []string{"Local Only Story", "Another Local Story"} {
			book := &models.Book{ForeignID: "OL-L" + strconv.Itoa(i), AuthorID: author.ID, Title: title, SortTitle: title, Status: models.BookStatusWanted, Genres: []string{}}
			if err := f.books.Create(ctx, book); err != nil {
				t.Fatal(err)
			}
			if _, err := f.series.LinkBookIfMissing(ctx, s.ID, book.ID, strconv.Itoa(i+5), true); err != nil {
				t.Fatal(err)
			}
		}

		rec := httptest.NewRecorder()
		f.h.AutoLinkHardcover(rec, covSrSeriesRequest(http.MethodPost, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
		var resp seriesHardcoverAutoResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.Linked || resp.Link == nil {
			t.Fatalf("response = %+v, want linked on author agreement", resp)
		}
		top := resp.Candidates[0]
		if top.Slug != "lantern-chronicles" || top.Title != "Lantern Chronicles" || top.AuthorName != "Mara Quill" || top.BookCount != 1 {
			t.Fatalf("candidate not filled from catalog: %+v", top)
		}
		stored, err := f.series.GetHardcoverLink(ctx, s.ID)
		if err != nil || stored == nil {
			t.Fatalf("link not stored: %v %v", stored, err)
		}
		if stored.LinkedBy != "auto" || stored.HardcoverSeriesID != catalog.ForeignID || stored.HardcoverSlug != "lantern-chronicles" {
			t.Fatalf("stored link = %+v", stored)
		}
	})
}

// TestSeriesPutHardcoverLinkWithoutCatalog covers the two ways a manual link
// is stored from the request body alone: no aggregator, and an aggregator
// that has no catalog for the id. A non-positive confidence defaults to 1.
func TestSeriesPutHardcoverLinkWithoutCatalog(t *testing.T) {
	for name, provider := range map[string]*stubSeriesProvider{
		"no aggregator":     nil,
		"catalog not found": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := covSrNewSeriesFixture(t, provider)
			s := f.createSeries(t, "manual:series:put", "Put")
			body := `{"foreignId":"  hc-series:777  ","slug":" put-slug ","title":"Echoed Title","authorName":"Echo","bookCount":3,"confidence":-2}`
			rec := httptest.NewRecorder()
			f.h.PutHardcoverLink(rec, covSrSeriesRequest(http.MethodPut, body, map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
			}
			stored, err := f.series.GetHardcoverLink(context.Background(), s.ID)
			if err != nil || stored == nil {
				t.Fatalf("link not stored: %v %v", stored, err)
			}
			if stored.HardcoverSeriesID != "hc-series:777" || stored.HardcoverProviderID != "777" ||
				stored.HardcoverSlug != "put-slug" || stored.HardcoverTitle != "Echoed Title" ||
				stored.Confidence != 1 || stored.LinkedBy != "manual" {
				t.Fatalf("stored link = %+v", stored)
			}
		})
	}
}

func TestSeriesHardcoverDiffRequiresLink(t *testing.T) {
	f := covSrNewSeriesFixture(t, &stubSeriesProvider{})
	t.Run("missing series", func(t *testing.T) {
		rec := httptest.NewRecorder()
		f.h.HardcoverDiff(rec, covSrSeriesRequest(http.MethodGet, "", map[string]string{"id": "9999"}))
		if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "series not found" {
			t.Fatalf("status = %d %s, want 404 series not found", rec.Code, rec.Body.String())
		}
	})
	t.Run("unlinked series", func(t *testing.T) {
		s := f.createSeries(t, "manual:series:unlinked", "Unlinked")
		rec := httptest.NewRecorder()
		f.h.HardcoverDiff(rec, covSrSeriesRequest(http.MethodGet, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusNotFound || covSrDecodeError(t, rec) != "hardcover link not found" {
			t.Fatalf("status = %d %s, want 404 hardcover link not found", rec.Code, rec.Body.String())
		}
	})
	t.Run("catalog vanished upstream", func(t *testing.T) {
		s := f.createSeries(t, "manual:series:gone", "Gone")
		f.link(t, s.ID, "hc-series:gone")
		rec := httptest.NewRecorder()
		f.h.HardcoverDiff(rec, covSrSeriesRequest(http.MethodGet, "", map[string]string{"id": strconv.FormatInt(s.ID, 10)}))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d %s, want 502", rec.Code, rec.Body.String())
		}
	})
}

// TestSeriesFillSingleBookRefusals covers the selector form of Fill (one
// catalog book) when it cannot resolve that book. None of them may create a
// book or queue a search.
func TestSeriesFillSingleBookRefusals(t *testing.T) {
	ctx := context.Background()
	catalog := &metadata.SeriesCatalog{
		ForeignID: "hc-series:fill", Title: "Fill Series", AuthorName: "Fill Author",
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:fill-1", Title: "Fill One", Position: "1",
			Book: models.Book{ForeignID: "hc:fill-1", Title: "Fill One"},
		}},
	}
	cases := []struct {
		name     string
		provider *stubSeriesProvider
		setup    func(t *testing.T, f *covSrSeriesFixture) int64
		body     string
		wantCode int
		wantErr  string
	}{
		{
			name:     "no aggregator",
			provider: nil,
			setup:    func(t *testing.T, f *covSrSeriesFixture) int64 { return f.createSeries(t, "manual:a", "A").ID },
			body:     `{"position":"1"}`,
			wantCode: http.StatusInternalServerError,
			wantErr:  "internal server error",
		},
		{
			name:     "missing series",
			provider: &stubSeriesProvider{},
			setup:    func(*testing.T, *covSrSeriesFixture) int64 { return 9999 },
			body:     `{"position":"1"}`,
			wantCode: http.StatusNotFound,
			wantErr:  "series not found",
		},
		{
			name:     "series has no hardcover link",
			provider: &stubSeriesProvider{},
			setup:    func(t *testing.T, f *covSrSeriesFixture) int64 { return f.createSeries(t, "manual:b", "B").ID },
			body:     `{"foreignBookId":"hc:fill-1"}`,
			wantCode: http.StatusNotFound,
			wantErr:  "hardcover book not found in missing catalog",
		},
		{
			name:     "catalog provider failure",
			provider: &stubSeriesProvider{catalogErr: errors.New("hardcover down")},
			setup: func(t *testing.T, f *covSrSeriesFixture) int64 {
				s := f.createSeries(t, "manual:c", "C")
				f.link(t, s.ID, catalog.ForeignID)
				return s.ID
			},
			body:     `{"position":"1"}`,
			wantCode: http.StatusBadGateway,
			wantErr:  "metadata provider unavailable",
		},
		{
			name:     "catalog missing upstream",
			provider: &stubSeriesProvider{},
			setup: func(t *testing.T, f *covSrSeriesFixture) int64 {
				s := f.createSeries(t, "manual:d", "D")
				f.link(t, s.ID, catalog.ForeignID)
				return s.ID
			},
			body:     `{"position":"1"}`,
			wantCode: http.StatusNotFound,
			wantErr:  "hardcover book not found in missing catalog",
		},
		{
			name:     "selector matches nothing missing",
			provider: &stubSeriesProvider{catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog}},
			setup: func(t *testing.T, f *covSrSeriesFixture) int64 {
				s := f.createSeries(t, "manual:e", "E")
				f.link(t, s.ID, catalog.ForeignID)
				return s.ID
			},
			body:     `{"foreignBookId":"hc:not-in-catalog","providerId":"zzz"}`,
			wantCode: http.StatusNotFound,
			wantErr:  "hardcover book not found in missing catalog",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := covSrNewSeriesFixture(t, tc.provider)
			id := tc.setup(t, f)
			rec := httptest.NewRecorder()
			f.h.Fill(rec, covSrSeriesRequest(http.MethodPost, tc.body, map[string]string{"id": strconv.FormatInt(id, 10)}))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := covSrDecodeError(t, rec); got != tc.wantErr {
				t.Fatalf("error = %q, want %q", got, tc.wantErr)
			}
			if b, _ := f.books.GetByForeignID(ctx, "hc:fill-1"); b != nil {
				t.Fatalf("refused fill created %+v", b)
			}
			select {
			case b := <-f.searcher.ch:
				t.Fatalf("refused fill queued a search for %q", b.Title)
			default:
			}
		})
	}
}

func TestSeriesTitleTiebreakHelpers(t *testing.T) {
	cases := []struct {
		name                       string
		candidate, current, target string
		want                       bool
	}{
		{"candidate is the exact title", "The Way of Kings", "Kings", "The Way of Kings", true},
		{"current is the exact title", "Kings", "The Way of Kings", "The Way of Kings", false},
		{"candidate carries a series suffix", "The Way of Kings Stormlight Archive Book One", "Kings and Queens of Old", "The Way of Kings", true},
		{"current carries a series suffix", "Kings and Queens of Old", "The Way of Kings Stormlight Archive Book One", "The Way of Kings", false},
		{"closer spelling wins on ratio", "Mistborne", "Elantris", "Mistborn", true},
		{"farther spelling loses on ratio", "Elantris", "Mistborne", "Mistborn", false},
		{"identical cleaned titles fall back to length", "Dune", "Dune!!!", "Dune", true},
		{"identical cleaned titles, longer loses", "Dune!!!", "Dune", "Dune", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleTiebreakWins(tc.candidate, tc.current, tc.target); got != tc.want {
				t.Fatalf("titleTiebreakWins(%q, %q, %q) = %v, want %v", tc.candidate, tc.current, tc.target, got, tc.want)
			}
		})
	}

	// A candidate shorter than the target exercises absInt's negative branch.
	if !titleLengthCloser("ab", "abcdefgh", "abcd") {
		t.Fatal("titleLengthCloser: 2 away should beat 4 away")
	}
	if titleLengthCloser("abcdefgh", "ab", "abcd") {
		t.Fatal("titleLengthCloser: 4 away should not beat 2 away")
	}
	if absInt(-3) != 3 || absInt(3) != 3 {
		t.Fatal("absInt")
	}
	if titleBoundaryMatch("anything", "") || titleBoundaryMatch("same", "same") {
		t.Fatal("titleBoundaryMatch must refuse an empty target and equality")
	}
}

func TestSeriesHardcoverAuthorFallbackID(t *testing.T) {
	if got := hardcoverAuthorFallbackID("Mara Quill"); !strings.HasPrefix(got, "hc-author:") || !strings.Contains(got, "-") || strings.Contains(got, " ") {
		t.Fatalf("fallback id = %q, want hc-author:<dashed key>", got)
	}
	if got := hardcoverAuthorFallbackID(""); got != "hc-author:unknown" {
		t.Fatalf("blank name fallback = %q, want hc-author:unknown", got)
	}
}

func TestSeriesFindCatalogBook(t *testing.T) {
	books := []metadata.SeriesCatalogBook{
		{ForeignID: "hc:boxset", Position: "1", Title: "Box Set"},
		{ForeignID: "", Position: "2", Title: "Nested", Book: models.Book{ForeignID: "hc:nested"}},
		{ForeignID: "hc:real-one", Position: "1", Title: "Real One"},
	}
	if got, ok := findCatalogBook(books, "hc:real-one", "1"); !ok || got.Title != "Real One" {
		t.Fatalf("exact id = %+v %v, want Real One over the position-1 box set", got, ok)
	}
	if got, ok := findCatalogBook(books, "hc:nested", ""); !ok || got.Title != "Nested" {
		t.Fatalf("nested book id = %+v %v, want Nested", got, ok)
	}
	if got, ok := findCatalogBook(books, "hc:unknown", "2"); !ok || got.Title != "Nested" {
		t.Fatalf("position fallback = %+v %v, want Nested", got, ok)
	}
	if _, ok := findCatalogBook(books, "hc:unknown", "9"); ok {
		t.Fatal("no id or position match must report not found")
	}
}
