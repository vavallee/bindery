package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOPDSSeriesCoverage covers the series navigation feed and the single
// series feed's id and existence checks, all behind the OPDS auth middleware.
func TestOPDSSeriesCoverage(t *testing.T) {
	r, _, _, apiKey := opdsFixture(t)

	body := doOK(t, r, "/opds/series", apiKey)
	if !strings.Contains(body, "<feed") || !strings.Contains(body, "/opds/series") {
		t.Fatalf("series feed is not an atom feed with a self link: %s", body)
	}

	for _, c := range []struct {
		path string
		want int
	}{
		{"/opds/series/abc", http.StatusBadRequest},
		{"/opds/series/424242", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Header.Set("X-Api-Key", apiKey)
		r.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("GET %s = %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.want)
		}
	}

	// Without credentials the feed is refused before the handler runs.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/opds/series", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous series feed = %d, want 401", rec.Code)
	}
}
