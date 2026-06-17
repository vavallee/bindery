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

func TestLookupByISBN(t *testing.T) {
	p := &stubProvider{byISBN: &models.Book{Title: "Hyperion", Description: "A long-enough description to skip the enrichment branch in the aggregator."}}
	h := NewSearchHandler(metadata.NewAggregator(p))

	// Missing isbn
	rec := httptest.NewRecorder()
	h.LookupByISBN(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/isbn", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing isbn: expected 400, got %d", rec.Code)
	}

	// Success
	rec = httptest.NewRecorder()
	h.LookupByISBN(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/isbn?isbn=9780553283686", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Not found — fresh aggregator so nothing is cached
	p2 := &stubProvider{byISBN: nil}
	h2 := NewSearchHandler(metadata.NewAggregator(p2))
	rec = httptest.NewRecorder()
	h2.LookupByISBN(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/isbn?isbn=0000000000", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("not found: expected 404, got %d", rec.Code)
	}

	// Error
	p3 := &stubProvider{byISBNErr: errors.New("net down")}
	h3 := NewSearchHandler(metadata.NewAggregator(p3))
	rec = httptest.NewRecorder()
	h3.LookupByISBN(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search/isbn?isbn=fail", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("error: expected 502, got %d", rec.Code)
	}
}
