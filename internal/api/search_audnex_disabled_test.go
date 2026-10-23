package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/audnex"
)

// TestLookup_ASIN_AudnexDisabledSaysSo covers #2015: with audnex turned off the
// ASIN lookup is skipped, so the 404 must say why instead of claiming that no
// book exists. An ordinary miss keeps its wording.
func TestLookup_ASIN_AudnexDisabledSaysSo(t *testing.T) {
	ax := &stubAudnexClient{books: map[string]*audnex.Book{"B0DBJBFHGT": {ASIN: "B0DBJBFHGT", Title: "Iron Flame"}}}
	agg := metadata.NewAggregator(&stubProvider{}).WithAudnexClient(ax)
	h := NewSearchHandler(agg, nil, nil)

	agg.SetAudiobookSourcesDisabled(false, true)
	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?asin=B0DBJBFHGT", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "audnex source is turned off") || strings.Contains(body, "No book found") {
		t.Errorf("disabled message = %s", body)
	}

	agg.SetAudiobookSourcesDisabled(false, false)
	rec = httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?asin=B0NONEXIST", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No book found for ASIN") {
		t.Errorf("ordinary miss = %d %s", rec.Code, rec.Body.String())
	}
}
