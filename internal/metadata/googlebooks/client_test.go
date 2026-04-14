package googlebooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc implements http.RoundTripper for test servers without needing httptest.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// mockResponse builds a 200-OK HTTP response with a JSON-encoded body.
func mockResponse(t *testing.T, statusCode int, body interface{}) *http.Response {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			t.Fatalf("mockResponse: marshal: %v", err)
		}
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(string(raw))),
		Header:     make(http.Header),
	}
}

// newMockClient creates a googlebooks Client with a transport that routes on URL path.
func newMockClient(handler func(r *http.Request) *http.Response) *Client {
	return &Client{
		http:   &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return handler(r), nil })},
		apiKey: "",
	}
}

func TestName(t *testing.T) {
	c := New("")
	if c.Name() != "googlebooks" {
		t.Errorf("Name: want 'googlebooks', got %q", c.Name())
	}
}

func TestSearchBooks_Success(t *testing.T) {
	resp := volumeSearchResponse{
		TotalItems: 2,
		Items: []volumeItem{
			{
				ID: "vol001",
				VolumeInfo: volumeInfo{
					Title:       "Dune",
					Authors:     []string{"Frank Herbert"},
					Description: "A science fiction novel.",
					Categories:  []string{"Fiction"},
					ImageLinks:  &imageLinks{Thumbnail: "http://books.google.com/cover1.jpg"},
				},
			},
			{
				ID: "vol002",
				VolumeInfo: volumeInfo{
					Title:   "Dune Messiah",
					Authors: []string{"Frank Herbert"},
				},
			},
		},
	}

	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusOK, resp)
	})

	books, err := c.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("expected 2 books, got %d", len(books))
	}
	if books[0].Title != "Dune" {
		t.Errorf("first title: want 'Dune', got %q", books[0].Title)
	}
	if books[0].ForeignID != "gb:vol001" {
		t.Errorf("ForeignID: want 'gb:vol001', got %q", books[0].ForeignID)
	}
	// Thumbnail should be upgraded to HTTPS
	if !strings.HasPrefix(books[0].ImageURL, "https://") {
		t.Errorf("ImageURL not upgraded to HTTPS: %q", books[0].ImageURL)
	}
	if books[0].Author == nil || books[0].Author.Name != "Frank Herbert" {
		t.Errorf("Author: %+v", books[0].Author)
	}
}

func TestSearchBooks_EmptyResult(t *testing.T) {
	resp := volumeSearchResponse{TotalItems: 0, Items: nil}
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusOK, resp)
	})

	books, err := c.SearchBooks(context.Background(), "nonexistent book xyz")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("expected 0 books, got %d", len(books))
	}
}

func TestSearchBooks_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusTooManyRequests, "rate limited")
	})

	_, err := c.SearchBooks(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 429")
	}
}

func TestSearchBooks_WithAPIKey(t *testing.T) {
	var gotURL string
	c := &Client{
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return mockResponse(t, http.StatusOK, volumeSearchResponse{}), nil
		})},
		apiKey: "myapikey",
	}

	_, _ = c.SearchBooks(context.Background(), "test")
	if !strings.Contains(gotURL, "key=myapikey") {
		t.Errorf("expected API key in URL, got: %q", gotURL)
	}
}

func TestSearchAuthors_Deduplicates(t *testing.T) {
	// Two books by the same author → should yield one Author record.
	resp := volumeSearchResponse{
		Items: []volumeItem{
			{ID: "v1", VolumeInfo: volumeInfo{Title: "Book A", Authors: []string{"Jane Doe"}}},
			{ID: "v2", VolumeInfo: volumeInfo{Title: "Book B", Authors: []string{"Jane Doe"}}},
			{ID: "v3", VolumeInfo: volumeInfo{Title: "Book C", Authors: []string{"John Smith"}}},
		},
	}
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusOK, resp)
	})

	authors, err := c.SearchAuthors(context.Background(), "Jane Doe")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 2 {
		t.Errorf("expected 2 unique authors, got %d: %+v", len(authors), authors)
	}
}

func TestSearchAuthors_SearchError(t *testing.T) {
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusInternalServerError, "error")
	})

	_, err := c.SearchAuthors(context.Background(), "anyone")
	if err == nil {
		t.Fatal("expected error to propagate from SearchBooks")
	}
}

func TestGetAuthor_Unsupported(t *testing.T) {
	c := New("")
	_, err := c.GetAuthor(context.Background(), "some-id")
	if err == nil {
		t.Fatal("expected error: Google Books does not support author lookup by ID")
	}
}

func TestGetEditions_Nil(t *testing.T) {
	c := New("")
	editions, err := c.GetEditions(context.Background(), "any")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if editions != nil {
		t.Errorf("expected nil editions, got %v", editions)
	}
}

func TestGetBook_Success(t *testing.T) {
	item := volumeItem{
		ID: "vol999",
		VolumeInfo: volumeInfo{
			Title:         "Foundation",
			Authors:       []string{"Isaac Asimov"},
			Description:   "A classic sci-fi series.",
			AverageRating: 4.3,
			RatingsCount:  5000,
		},
	}
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusOK, item)
	})

	book, err := c.GetBook(context.Background(), "vol999")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Title != "Foundation" {
		t.Errorf("Title: want 'Foundation', got %q", book.Title)
	}
	if book.ForeignID != "gb:vol999" {
		t.Errorf("ForeignID: want 'gb:vol999', got %q", book.ForeignID)
	}
	if book.AverageRating != 4.3 {
		t.Errorf("AverageRating: want 4.3, got %f", book.AverageRating)
	}
}

func TestGetBook_WithAPIKey(t *testing.T) {
	var gotURL string
	c := &Client{
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return mockResponse(t, http.StatusOK, volumeItem{}), nil
		})},
		apiKey: "testkey",
	}

	_, _ = c.GetBook(context.Background(), "vol1")
	if !strings.Contains(gotURL, "key=testkey") {
		t.Errorf("expected API key in GetBook URL, got: %q", gotURL)
	}
}

func TestGetBook_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusNotFound, "not found")
	})

	_, err := c.GetBook(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestGetBookByISBN_Found(t *testing.T) {
	var gotURL string
	resp := volumeSearchResponse{
		Items: []volumeItem{
			{ID: "isbnvol", VolumeInfo: volumeInfo{Title: "ISBN Book"}},
		},
	}
	c := &Client{
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return mockResponse(t, http.StatusOK, resp), nil
		})},
	}

	book, err := c.GetBookByISBN(context.Background(), "9780441013593")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.Title != "ISBN Book" {
		t.Errorf("Title: want 'ISBN Book', got %q", book.Title)
	}
	if !strings.Contains(gotURL, "isbn%3A9780441013593") && !strings.Contains(gotURL, "isbn:9780441013593") {
		t.Errorf("expected isbn operator in search URL, got: %q", gotURL)
	}
}

func TestGetBookByISBN_NotFound(t *testing.T) {
	c := newMockClient(func(r *http.Request) *http.Response {
		return mockResponse(t, http.StatusOK, volumeSearchResponse{})
	})

	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book != nil {
		t.Errorf("expected nil for missing ISBN, got %+v", book)
	}
}

func TestVolumeToBook_NoAuthor(t *testing.T) {
	c := New("")
	item := volumeItem{
		ID:         "solo",
		VolumeInfo: volumeInfo{Title: "No Author Book"},
	}
	b := c.volumeToBook(item)
	if b.Author != nil {
		t.Errorf("expected nil Author when no authors in response, got %+v", b.Author)
	}
	if b.Genres == nil {
		t.Error("Genres should be empty slice, not nil")
	}
}

func TestVolumeToBook_HTTPSUpgrade(t *testing.T) {
	c := New("")
	item := volumeItem{
		ID: "v",
		VolumeInfo: volumeInfo{
			ImageLinks: &imageLinks{Thumbnail: "http://books.google.com/img.jpg"},
		},
	}
	b := c.volumeToBook(item)
	if !strings.HasPrefix(b.ImageURL, "https://") {
		t.Errorf("expected HTTPS image URL, got %q", b.ImageURL)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Frank Herbert", "Herbert, Frank"},
		{"J.K. Rowling", "Rowling, J.K."},
		{"Madonna", "Madonna"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sortName(tt.input)
		if got != tt.want {
			t.Errorf("sortName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
