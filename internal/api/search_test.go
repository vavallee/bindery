package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
