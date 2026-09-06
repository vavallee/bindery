package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// seedSeriesForPaging creates n series, each with one linked book, titled so
// that title order matches creation order.
func seedSeriesForPaging(t *testing.T, seriesRepo *db.SeriesRepo, authorRepo *db.AuthorRepo, bookRepo *db.BookRepo, n int) {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-PG-A", Name: "Pager", SortName: "Pager"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		suffix := strconv.Itoa(100 + i)[1:] // "01".."99"
		book := &models.Book{
			ForeignID: "OL-PG-B" + suffix,
			AuthorID:  author.ID,
			Title:     "Book " + suffix,
			Status:    models.BookStatusWanted,
		}
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		s := &models.Series{ForeignID: "OL-PG-S" + suffix, Title: "Series " + suffix}
		if err := seriesRepo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSeriesList_DefaultStaysABareArray is the compatibility guard for #2345.
// Pagination is opt-in, so a request with no limit and no offset must still
// return the bare array web/src/api/series.ts expects.
func TestSeriesList_DefaultStaysABareArray(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	seedSeriesForPaging(t, seriesRepo, authorRepo, bookRepo, 5)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Fatalf("body is not a JSON array: %s", rec.Body.String())
	}
	var list []models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 5 {
		t.Errorf("got %d series, want all 5", len(list))
	}
	for _, s := range list {
		if len(s.Books) != 1 {
			t.Errorf("series %q carries %d books, want 1", s.Title, len(s.Books))
		}
	}
}

// TestSeriesList_Paginated covers the opt-in envelope: limit and offset walk
// the collection in title order and every page reports the full total.
func TestSeriesList_Paginated(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	seedSeriesForPaging(t, seriesRepo, authorRepo, bookRepo, 5)

	get := func(query string) seriesListResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for ?%s, want 200", rec.Code, query)
		}
		var resp seriesListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode ?%s: %v (body %s)", query, err, rec.Body.String())
		}
		return resp
	}

	first := get("limit=2")
	if first.Total != 5 || first.Limit != 2 || first.Offset != 0 {
		t.Errorf("first page envelope = %+v, want total 5 limit 2 offset 0", first)
	}
	if len(first.Items) != 2 || first.Items[0].Title != "Series 01" || first.Items[1].Title != "Series 02" {
		t.Fatalf("first page items = %v", titlesOf(first.Items))
	}
	if len(first.Items[0].Books) != 1 {
		t.Errorf("paged series lost its books: %+v", first.Items[0].Books)
	}

	second := get("limit=2&offset=2")
	if len(second.Items) != 2 || second.Items[0].Title != "Series 03" {
		t.Errorf("second page items = %v", titlesOf(second.Items))
	}

	// offset alone opts in too, at the default limit.
	tail := get("offset=4")
	if tail.Limit != seriesListDefaultLimit {
		t.Errorf("offset-only limit = %d, want the default %d", tail.Limit, seriesListDefaultLimit)
	}
	if len(tail.Items) != 1 || tail.Items[0].Title != "Series 05" {
		t.Errorf("offset-only page = %v", titlesOf(tail.Items))
	}

	// Past the end is an empty items array, not null and not an error.
	past := get("limit=2&offset=99")
	if past.Total != 5 || past.Items == nil || len(past.Items) != 0 {
		t.Errorf("past-the-end page = %+v, want total 5 and an empty items array", past)
	}

	// An over-large limit clamps rather than erroring.
	clamped := get("limit=100000")
	if clamped.Limit != seriesListMaxLimit {
		t.Errorf("limit = %d, want it clamped to %d", clamped.Limit, seriesListMaxLimit)
	}
}

func titlesOf(series []models.Series) []string {
	out := make([]string, len(series))
	for i, s := range series {
		out[i] = s.Title
	}
	return out
}
