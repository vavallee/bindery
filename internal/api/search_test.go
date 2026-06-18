package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// stubProvider implements metadata.Provider for search handler tests.
type stubProvider struct {
	authors    []models.Author
	authorsErr error
	books      []models.Book
	booksErr   error
	byISBN     *models.Book
	byISBNErr  error
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) SearchAuthors(context.Context, string) ([]models.Author, error) {
	return s.authors, s.authorsErr
}
func (s *stubProvider) SearchBooks(context.Context, string) ([]models.Book, error) {
	return s.books, s.booksErr
}
func (s *stubProvider) GetAuthor(context.Context, string) (*models.Author, error) {
	return nil, nil
}
func (s *stubProvider) GetBook(context.Context, string) (*models.Book, error) {
	return nil, nil
}
func (s *stubProvider) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}
func (s *stubProvider) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return s.byISBN, s.byISBNErr
}

// TestWriteUpstreamErrorDoesNotLeakSecrets proves the client-facing body for an
// upstream/transport failure is the generic message and never contains the
// underlying error string. A Google Books transport error wraps a *url.Error
// whose Error() embeds the request URL, which carries the API key (?key=...)
// and the internal DNS resolver IP. Echoing that back leaked both (#1144).
func TestWriteUpstreamErrorDoesNotLeakSecrets(t *testing.T) {
	const (
		secretKey  = "AIzaSECRET_KEY_VALUE"
		resolverIP = "10.0.0.53"
	)
	leaky := fmt.Errorf(`search books: Get "https://www.googleapis.com/books/v1/volumes?q=dune&key=%s": dial tcp: lookup www.googleapis.com on %s:53: i/o timeout`, secretKey, resolverIP)

	rec := httptest.NewRecorder()
	writeUpstreamError(rec, leaky)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	raw := rec.Body.String()
	var body map[string]string
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "metadata provider unavailable" {
		t.Fatalf("expected generic message, got %q", body["error"])
	}
	for _, leak := range []string{secretKey, resolverIP, "googleapis.com", "key=", "dial tcp"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("client response leaked %q: %s", leak, raw)
		}
	}
}

func TestSearchAuthors(t *testing.T) {
	p := &stubProvider{authors: []models.Author{{Name: "Frank Herbert"}}}
	h := NewSearchHandler(metadata.NewAggregator(p))

	// Missing term
	rec := httptest.NewRecorder()
	h.SearchAuthors(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/author", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing term: expected 400, got %d", rec.Code)
	}

	// Success
	rec = httptest.NewRecorder()
	h.SearchAuthors(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/author?term=herbert", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []models.Author
	json.NewDecoder(rec.Body).Decode(&got)
	if len(got) != 1 || got[0].Name != "Frank Herbert" {
		t.Errorf("unexpected authors: %+v", got)
	}

	// Error propagation
	p.authorsErr = errors.New("upstream down")
	rec = httptest.NewRecorder()
	h.SearchAuthors(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/author?term=x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("error: expected 502, got %d", rec.Code)
	}
}

func TestSearchBooks(t *testing.T) {
	p := &stubProvider{books: []models.Book{{Title: "Dune"}}}
	h := NewSearchHandler(metadata.NewAggregator(p))

	// Missing term
	rec := httptest.NewRecorder()
	h.SearchBooks(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/book", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing term: expected 400, got %d", rec.Code)
	}

	// Success
	rec = httptest.NewRecorder()
	h.SearchBooks(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/book?term=dune", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Error
	p.booksErr = errors.New("upstream down")
	rec = httptest.NewRecorder()
	h.SearchBooks(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/book?term=x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("error: expected 502, got %d", rec.Code)
	}
}

// TestSearchBooksEmptyIsArray guards against the nil-slice bug from #1188: a
// provider that succeeds but returns no rows must serialize as `[]`, not `null`,
// or the Add Book modal crashes calling `.map()` on a null body.
func TestSearchBooksEmptyIsArray(t *testing.T) {
	p := &stubProvider{books: nil} // success, but no results
	h := NewSearchHandler(metadata.NewAggregator(p))

	rec := httptest.NewRecorder()
	h.SearchBooks(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/book?term=zzznomatch", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("expected empty body to encode as [], got %q", body)
	}

	// And the aggregator itself returns a non-nil slice.
	got, err := metadata.NewAggregator(p).SearchBooks(context.Background(), "zzznomatch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Error("SearchBooks returned a nil slice for empty success; want non-nil empty slice")
	}
}

func TestLookup_ISBN(t *testing.T) {
	p := &stubProvider{byISBN: &models.Book{Title: "Hyperion", Description: "A long-enough description to skip the enrichment branch in the aggregator."}}
	h := NewSearchHandler(metadata.NewAggregator(p))

	// Neither isbn nor asin → 400 naming both params.
	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing params: expected 400, got %d", rec.Code)
	}
	var errBody map[string]string
	json.NewDecoder(rec.Body).Decode(&errBody)
	if msg := errBody["error"]; !strings.Contains(msg, "isbn") || !strings.Contains(msg, "asin") {
		t.Errorf("400 message should name both params, got %q", msg)
	}

	// Success — ISBN path unchanged.
	rec = httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?isbn=9780553283686", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Not found — fresh aggregator so nothing is cached
	p2 := &stubProvider{byISBN: nil}
	h2 := NewSearchHandler(metadata.NewAggregator(p2))
	rec = httptest.NewRecorder()
	h2.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?isbn=0000000000", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("not found: expected 404, got %d", rec.Code)
	}

	// Error
	p3 := &stubProvider{byISBNErr: errors.New("net down")}
	h3 := NewSearchHandler(metadata.NewAggregator(p3))
	rec = httptest.NewRecorder()
	h3.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?isbn=fail", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("error: expected 502, got %d", rec.Code)
	}
}

// stubAudnexClient drives the aggregator's ASIN canonicalization path without a
// network call: GetBook returns the mapped audnex.Book for an ASIN, or nil.
type stubAudnexClient struct {
	books map[string]*audnex.Book
}

func (s *stubAudnexClient) GetBook(_ context.Context, asin string) (*audnex.Book, error) {
	if s.books == nil {
		return nil, nil
	}
	return s.books[asin], nil
}

// TestLookup_ASIN_Success proves the ?asin= path returns 200 with the canonical
// book re-stamped into the ASIN-origin shape the Add Book modal expects: the
// canonical foreignBookId is preserved, MediaType is audiobook, and the ASIN is
// populated (the canonical OpenLibrary record carries neither on its own).
func TestLookup_ASIN_Success(t *testing.T) {
	primary := &stubProvider{
		books:  []models.Book{{ForeignID: "OL-IRON", Title: "Iron Flame", EditionCount: 42, Author: &models.Author{Name: "Rebecca Yarros"}}},
		byISBN: &models.Book{ForeignID: "OL-IRON", Title: "Iron Flame", Description: "Canonical OpenLibrary description for Iron Flame.", Author: &models.Author{Name: "Rebecca Yarros"}},
	}
	audnexClient := &stubAudnexClient{books: map[string]*audnex.Book{
		"B0DBJBFHGT": {
			ASIN:     "B0DBJBFHGT",
			Title:    "Iron Flame",
			Authors:  []audnex.Person{{Name: "Rebecca Yarros"}},
			Language: "English",
		},
	}}
	agg := metadata.NewAggregator(primary).WithAudnexClient(audnexClient)
	h := NewSearchHandler(agg)

	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?asin=B0DBJBFHGT", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got models.Book
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ForeignID != "OL-IRON" {
		t.Errorf("expected canonical foreignBookId OL-IRON preserved, got %q", got.ForeignID)
	}
	if got.MediaType != models.MediaTypeAudiobook {
		t.Errorf("expected mediaType %q, got %q", models.MediaTypeAudiobook, got.MediaType)
	}
	if got.ASIN != "B0DBJBFHGT" {
		t.Errorf("expected ASIN populated, got %q", got.ASIN)
	}
}

// TestLookup_ASIN_Miss proves a non-resolving ASIN returns 404 (the modal's
// normal empty/error state), not a 500 or a crash.
func TestLookup_ASIN_Miss(t *testing.T) {
	agg := metadata.NewAggregator(&stubProvider{}).WithAudnexClient(&stubAudnexClient{})
	h := NewSearchHandler(agg)

	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?asin=B0NONEXIST", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ASIN miss: expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}
