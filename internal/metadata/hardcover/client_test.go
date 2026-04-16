package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// testTransport routes all HTTP calls through a handler function.
type testTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (t *testTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.handler(r)
}

// gqlResponse builds a GraphQL-style HTTP response with the given data payload.
func gqlResponse(t *testing.T, statusCode int, data interface{}) *http.Response {
	t.Helper()
	var body []byte
	if s, ok := data.(string); ok {
		body = []byte(s)
	} else {
		wrapped := map[string]interface{}{"data": data}
		var err error
		body, err = json.Marshal(wrapped)
		if err != nil {
			t.Fatalf("gqlResponse: %v", err)
		}
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}
}

// newMockClient creates a hardcover Client backed by a custom transport.
func newMockClient(handler func(*http.Request) (*http.Response, error)) *Client {
	return &Client{
		http: &http.Client{Transport: &testTransport{handler: handler}},
	}
}

func TestName(t *testing.T) {
	c := New()
	if c.Name() != "hardcover" {
		t.Errorf("Name: want 'hardcover', got %q", c.Name())
	}
}

func TestSearchAuthors_Success(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"authors": []map[string]interface{}{
				{
					"id":   1,
					"name": "Brandon Sanderson",
					"slug": "brandon-sanderson",
					"bio":  "Fantasy author",
					"image": map[string]interface{}{
						"url": "https://example.com/bs.jpg",
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	authors, err := c.SearchAuthors(context.Background(), "Sanderson")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("expected 1 author, got %d", len(authors))
	}
	if authors[0].Name != "Brandon Sanderson" {
		t.Errorf("Name: want 'Brandon Sanderson', got %q", authors[0].Name)
	}
	if authors[0].ForeignID != "hc:brandon-sanderson" {
		t.Errorf("ForeignID: want 'hc:brandon-sanderson', got %q", authors[0].ForeignID)
	}
	if authors[0].ImageURL != "https://example.com/bs.jpg" {
		t.Errorf("ImageURL: got %q", authors[0].ImageURL)
	}
}

func TestSearchAuthors_Empty(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"authors": []interface{}{}}), nil
	})

	authors, err := c.SearchAuthors(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 0 {
		t.Errorf("expected 0 authors, got %d", len(authors))
	}
}

func TestSearchAuthors_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("server error")),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.SearchAuthors(context.Background(), "anyone")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSearchBooks_Success(t *testing.T) {
	year := 2001
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"books": []map[string]interface{}{
				{
					"id":            42,
					"title":         "Mistborn",
					"slug":          "mistborn",
					"description":   "A fantasy novel",
					"release_year":  year,
					"rating":        4.2,
					"ratings_count": 8000,
					"image":         map[string]interface{}{"url": "https://img.example.com/m.jpg"},
					"contributions": []map[string]interface{}{
						{"author": map[string]interface{}{"id": 1, "name": "Brandon Sanderson", "slug": "brandon-sanderson"}},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	books, err := c.SearchBooks(context.Background(), "Mistborn")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	b := books[0]
	if b.Title != "Mistborn" {
		t.Errorf("Title: want 'Mistborn', got %q", b.Title)
	}
	if b.ForeignID != "hc:mistborn" {
		t.Errorf("ForeignID: want 'hc:mistborn', got %q", b.ForeignID)
	}
	if b.AverageRating != 4.2 {
		t.Errorf("AverageRating: want 4.2, got %f", b.AverageRating)
	}
	if b.ReleaseDate == nil || b.ReleaseDate.Year() != 2001 {
		t.Errorf("ReleaseDate: expected year 2001")
	}
	if b.Author == nil || b.Author.Name != "Brandon Sanderson" {
		t.Errorf("Author: %+v", b.Author)
	}
	if b.ImageURL != "https://img.example.com/m.jpg" {
		t.Errorf("ImageURL: got %q", b.ImageURL)
	}
}

func TestSearchBooks_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("unavailable")),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.SearchBooks(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestGetAuthor_Found(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"authors": []map[string]interface{}{
				{"id": 5, "name": "Neil Gaiman", "slug": "neil-gaiman", "bio": "Writer", "image": nil},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	author, err := c.GetAuthor(context.Background(), "hc:neil-gaiman")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author == nil {
		t.Fatal("expected non-nil author")
	}
	if author.Name != "Neil Gaiman" {
		t.Errorf("Name: want 'Neil Gaiman', got %q", author.Name)
	}
	// ForeignID strips "hc:" for the query but toAuthor re-adds it
	if author.ForeignID != "hc:neil-gaiman" {
		t.Errorf("ForeignID: want 'hc:neil-gaiman', got %q", author.ForeignID)
	}
}

func TestGetAuthor_NotFound(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"authors": []interface{}{}}), nil
	})

	author, err := c.GetAuthor(context.Background(), "hc:nobody")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author != nil {
		t.Errorf("expected nil for missing author, got %+v", author)
	}
}

func TestGetAuthor_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(strings.NewReader("bad gateway")),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.GetAuthor(context.Background(), "hc:test")
	if err == nil {
		t.Fatal("expected error on 502")
	}
}

func TestGetBook_Found(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"books": []map[string]interface{}{
				{
					"id":            99,
					"title":         "American Gods",
					"slug":          "american-gods",
					"description":   "A novel about gods in America.",
					"rating":        4.1,
					"ratings_count": 12000,
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	book, err := c.GetBook(context.Background(), "hc:american-gods")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.Title != "American Gods" {
		t.Errorf("Title: want 'American Gods', got %q", book.Title)
	}
	if book.ForeignID != "hc:american-gods" {
		t.Errorf("ForeignID: want 'hc:american-gods', got %q", book.ForeignID)
	}
}

func TestGetBook_NotFound(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"books": []interface{}{}}), nil
	})

	book, err := c.GetBook(context.Background(), "hc:ghost-book")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book != nil {
		t.Errorf("expected nil for missing book, got %+v", book)
	}
}

func TestGetEditions_AlwaysNil(t *testing.T) {
	c := New()
	editions, err := c.GetEditions(context.Background(), "any-id")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if editions != nil {
		t.Errorf("expected nil, got %v", editions)
	}
}

func TestGetBookByISBN_Found(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					"book": map[string]interface{}{
						"id":    77,
						"title": "The Name of the Wind",
						"slug":  "the-name-of-the-wind",
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	book, err := c.GetBookByISBN(context.Background(), "9780756404741")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.Title != "The Name of the Wind" {
		t.Errorf("Title: want 'The Name of the Wind', got %q", book.Title)
	}
}

func TestGetBookByISBN_WithLanguage(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					"language": map[string]interface{}{"iso_639_1": "de"},
					"book": map[string]interface{}{
						"id":    88,
						"title": "Der Herr der Ringe",
						"slug":  "der-herr-der-ringe",
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	book, err := c.GetBookByISBN(context.Background(), "9783608938548")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.Language != "de" {
		t.Errorf("Language: want 'de', got %q", book.Language)
	}
}

func TestGetBookByISBN_NoLanguage(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					// no language field — should not panic
					"book": map[string]interface{}{
						"id":    89,
						"title": "Unknown Language Book",
						"slug":  "unknown-lang",
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	book, err := c.GetBookByISBN(context.Background(), "0000000000001")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
	}
	if book.Language != "" {
		t.Errorf("Language: want empty, got %q", book.Language)
	}
}

func TestGetBookByISBN_NotFound(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"editions": []interface{}{}}), nil
	})

	book, err := c.GetBookByISBN(context.Background(), "0000000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book != nil {
		t.Errorf("expected nil for missing ISBN, got %+v", book)
	}
}

func TestGetBookByISBN_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("forbidden")),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.GetBookByISBN(context.Background(), "9780756404741")
	if err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestToAuthor_NoSlug_UsesID(t *testing.T) {
	c := New()
	a := c.toAuthor(hcAuthor{ID: 42, Name: "Unknown Author", Slug: ""})
	if a.ForeignID != "hc:42" {
		t.Errorf("ForeignID: want 'hc:42', got %q", a.ForeignID)
	}
}

func TestToAuthor_NoImage(t *testing.T) {
	c := New()
	a := c.toAuthor(hcAuthor{ID: 1, Name: "Test", Slug: "test", Image: nil})
	if a.ImageURL != "" {
		t.Errorf("expected empty ImageURL when no image, got %q", a.ImageURL)
	}
}

func TestToBook_NoSlug_UsesID(t *testing.T) {
	c := New()
	b := c.toBook(hcBook{ID: 99, Title: "Slug-less Book", Slug: ""})
	if b.ForeignID != "hc:99" {
		t.Errorf("ForeignID: want 'hc:99', got %q", b.ForeignID)
	}
}

func TestToBook_ZeroReleaseYear(t *testing.T) {
	c := New()
	zero := 0
	b := c.toBook(hcBook{ID: 1, Title: "No Date", Slug: "no-date", ReleaseYear: &zero})
	if b.ReleaseDate != nil {
		t.Errorf("expected nil ReleaseDate for year=0, got %v", b.ReleaseDate)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Neil Gaiman", "Gaiman, Neil"},
		{"Brandon Sanderson", "Sanderson, Brandon"},
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

// --- WithToken / GetUserWishlist ---

func TestWithToken(t *testing.T) {
	c := New()
	if c.token != "" {
		t.Errorf("fresh client should have no token, got %q", c.token)
	}
	c2 := c.WithToken("secret-jwt")
	if c2.token != "secret-jwt" {
		t.Errorf("WithToken: want 'secret-jwt', got %q", c2.token)
	}
	// Original client must be unchanged.
	if c.token != "" {
		t.Errorf("WithToken should return a copy; original mutated to %q", c.token)
	}
	// Shared underlying HTTP client.
	if c.http != c2.http {
		t.Error("expected WithToken to share the underlying http.Client")
	}
}

func TestGetUserWishlist_NoToken(t *testing.T) {
	// Without a token, GetUserWishlist must return (nil, nil) without calling the API.
	called := false
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		called = true
		return gqlResponse(t, http.StatusOK, map[string]interface{}{}), nil
	})

	candidates, err := c.GetUserWishlist(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetUserWishlist: %v", err)
	}
	if candidates != nil {
		t.Errorf("expected nil candidates without token, got %v", candidates)
	}
	if called {
		t.Error("GetUserWishlist must not call the API without a token")
	}
}

func TestGetUserWishlist_Success(t *testing.T) {
	var gotAuth string
	year := 2019
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		data := map[string]interface{}{
			"me": map[string]interface{}{
				"user_books": []map[string]interface{}{
					{
						"book": map[string]interface{}{
							"id":            101,
							"title":         "Project Hail Mary",
							"slug":          "project-hail-mary",
							"description":   "An astronaut wakes up alone.",
							"release_year":  year,
							"rating":        4.7,
							"ratings_count": 50000,
							"image":         map[string]interface{}{"url": "https://img.example.com/phm.jpg"},
							"contributions": []map[string]interface{}{
								{"author": map[string]interface{}{"id": 7, "name": "Andy Weir", "slug": "andy-weir"}},
							},
						},
					},
					{
						"book": map[string]interface{}{
							"id":    102,
							"title": "Dune",
							"slug":  "dune",
						},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})
	c = c.WithToken("my-jwt")

	candidates, err := c.GetUserWishlist(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetUserWishlist: %v", err)
	}
	if gotAuth != "Bearer my-jwt" {
		t.Errorf("Authorization header: want 'Bearer my-jwt', got %q", gotAuth)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	first := candidates[0]
	if first.ForeignID != "hc:project-hail-mary" {
		t.Errorf("ForeignID: want 'hc:project-hail-mary', got %q", first.ForeignID)
	}
	if first.Title != "Project Hail Mary" {
		t.Errorf("Title: want 'Project Hail Mary', got %q", first.Title)
	}
	if first.AuthorName != "Andy Weir" {
		t.Errorf("AuthorName: want 'Andy Weir', got %q", first.AuthorName)
	}
	if first.Rating != 4.7 {
		t.Errorf("Rating: want 4.7, got %f", first.Rating)
	}
	if first.RatingsCount != 50000 {
		t.Errorf("RatingsCount: want 50000, got %d", first.RatingsCount)
	}
	if first.ReleaseDate == nil || first.ReleaseDate.Year() != 2019 {
		t.Errorf("ReleaseDate: expected 2019, got %v", first.ReleaseDate)
	}
	if first.MediaType != "ebook" {
		t.Errorf("MediaType: want 'ebook', got %q", first.MediaType)
	}
	if first.ImageURL != "https://img.example.com/phm.jpg" {
		t.Errorf("ImageURL: got %q", first.ImageURL)
	}

	// Second candidate: no contributions/image → empty author, empty URL.
	second := candidates[1]
	if second.AuthorName != "" {
		t.Errorf("second AuthorName should be empty, got %q", second.AuthorName)
	}
	if second.ImageURL != "" {
		t.Errorf("second ImageURL should be empty, got %q", second.ImageURL)
	}
}

func TestGetUserWishlist_DefaultLimit(t *testing.T) {
	// limit <= 0 must be coerced to 100 in the GraphQL variables.
	var gotVars map[string]any
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"me": map[string]interface{}{"user_books": []interface{}{}},
		}), nil
	})
	c = c.WithToken("t")

	_, err := c.GetUserWishlist(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetUserWishlist: %v", err)
	}
	// JSON numbers decode as float64.
	if v, ok := gotVars["limit"].(float64); !ok || v != 100 {
		t.Errorf("limit variable: want 100, got %v", gotVars["limit"])
	}
}

func TestGetUserWishlist_Empty(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"me": map[string]interface{}{"user_books": []interface{}{}},
		}), nil
	}).WithToken("t")

	candidates, err := c.GetUserWishlist(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetUserWishlist: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestGetUserWishlist_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader("bad token")),
			Header:     make(http.Header),
		}, nil
	}).WithToken("bad-token")

	_, err := c.GetUserWishlist(context.Background(), 10)
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestQuery_RequestFormat(t *testing.T) {
	var gotBody []byte
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{}}`)),
			Header:     make(http.Header),
		}, nil
	})

	var out struct{}
	_ = c.query(context.Background(), "query Test { __typename }", map[string]any{"key": "val"}, &out)

	var req gqlRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request body is not valid JSON: %v\nbody: %s", err, gotBody)
	}
	if !strings.Contains(req.Query, "Test") {
		t.Errorf("query field missing from request body: %q", req.Query)
	}
	if req.Variables["key"] != "val" {
		t.Errorf("variable 'key' not set: %v", req.Variables)
	}
}
