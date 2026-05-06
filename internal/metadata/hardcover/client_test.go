package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
)

// testTransport routes all HTTP calls through a handler function.
type testTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (t *testTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.handler(r)
}

type countingBodyReader struct {
	remaining int64
	read      int64
}

func (r *countingBodyReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	for i := range p {
		p[i] = 'x'
	}
	n := len(p)
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, nil
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

func TestNormalizeAPIToken(t *testing.T) {
	cases := map[string]string{
		"hc-secret":                         "hc-secret",
		"Bearer hc-secret":                  "hc-secret",
		"bearer hc-secret":                  "hc-secret",
		"Bearer Bearer hc-secret":           "hc-secret",
		" \n Bearer hc-secret \n\t":         "hc-secret",
		`"Bearer hc-secret"`:                "hc-secret",
		"Authorization: Bearer hc-secret":   "hc-secret",
		"authorization: hc-secret":          "hc-secret",
		"Authorization=Bearer Bearer token": "token",
		"Bearer":                            "",
		"Authorization: Bearer":             "",
	}
	for input, want := range cases {
		if got := NormalizeAPIToken(input); got != want {
			t.Errorf("NormalizeAPIToken(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQuery_UsesNormalizedAuthorizationHeader(t *testing.T) {
	for _, tt := range []struct {
		name   string
		client *Client
	}{
		{name: "token", client: newMockClient(nil).WithToken("Bearer Bearer hc-secret")},
		{name: "source", client: newMockClient(nil).WithTokenSource(func(context.Context) string { return "bearer hc-secret" })},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.client.http.Transport = &testTransport{handler: func(r *http.Request) (*http.Response, error) {
				if got := r.Header.Get("Authorization"); got != "Bearer hc-secret" {
					t.Fatalf("Authorization = %q, want Bearer hc-secret", got)
				}
				return gqlResponse(t, http.StatusOK, map[string]interface{}{"authors": []interface{}{}}), nil
			}}
			if _, err := tt.client.SearchAuthors(context.Background(), "nobody"); err != nil {
				t.Fatalf("SearchAuthors: %v", err)
			}
		})
	}
}

func TestQuery_GraphQLErrorsReturnError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, `{"errors":[{"message":"Malformed Authorization header","extensions":{"code":"invalid-headers"}}]}`), nil
	})
	_, err := c.SearchAuthors(context.Background(), "Sanderson")
	if err == nil {
		t.Fatal("expected GraphQL error")
	}
	if !strings.Contains(err.Error(), "Malformed Authorization header") || !strings.Contains(err.Error(), "invalid-headers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQuery_SuccessResponseBodyReadIsBounded(t *testing.T) {
	body := &countingBodyReader{remaining: hardcoverSuccessResponseBodyLimit + 1}
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(body),
			Header:     make(http.Header),
		}, nil
	})

	var out struct{}
	err := c.query(context.Background(), "query Test { __typename }", nil, &out)
	if err == nil {
		t.Fatal("expected truncated invalid JSON error")
	}
	if body.read != hardcoverSuccessResponseBodyLimit {
		t.Fatalf("read bytes = %d, want %d", body.read, hardcoverSuccessResponseBodyLimit)
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

func TestGetAuthorWorksByName_WithToken(t *testing.T) {
	var gotVars map[string]any
	var gotAuth string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		data := map[string]interface{}{
			"books": []map[string]interface{}{
				{
					"id":            10,
					"title":         "Dune",
					"slug":          "dune",
					"description":   "A desert planet.",
					"image":         map[string]interface{}{"url": "https://img/dune.jpg"},
					"release_year":  1965,
					"ratings_count": 1000,
					"rating":        4.5,
					"users_count":   2000,
					"genres":        []string{"Science Fiction"},
					"has_audiobook": true,
					"has_ebook":     false,
					"audio_seconds": 7200,
					"contributions": []map[string]interface{}{
						{"author": map[string]interface{}{"id": 1, "name": "Frank Herbert", "slug": "frank-herbert"}},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	}).WithToken("hc-secret")

	books, err := c.GetAuthorWorksByName(context.Background(), "Frank Herbert")
	if err != nil {
		t.Fatalf("GetAuthorWorksByName: %v", err)
	}
	if gotAuth != "Bearer hc-secret" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if gotVars["author"] != "Frank Herbert" {
		t.Fatalf("author variable = %v", gotVars["author"])
	}
	if len(books) != 1 {
		t.Fatalf("books len = %d, want 1", len(books))
	}
	book := books[0]
	if book.ForeignID != "hc:dune" || book.Title != "Dune" || book.ImageURL == "" {
		t.Fatalf("unexpected book: %+v", book)
	}
	if book.DurationSeconds != 7200 {
		t.Fatalf("DurationSeconds = %d, want 7200", book.DurationSeconds)
	}
	if len(book.Genres) != 1 || book.Genres[0] != "Science Fiction" {
		t.Fatalf("Genres = %+v", book.Genres)
	}
	if book.MediaType != "" {
		t.Fatalf("MediaType = %q, want empty author import default", book.MediaType)
	}
}

func TestGetAuthorWorksByName_NoTokenSkipsRequest(t *testing.T) {
	called := false
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		called = true
		return gqlResponse(t, http.StatusOK, map[string]interface{}{}), nil
	})

	books, err := c.GetAuthorWorksByName(context.Background(), "Frank Herbert")
	if !errors.Is(err, metadata.ErrProviderNotConfigured) {
		t.Fatalf("GetAuthorWorksByName error = %v, want ErrProviderNotConfigured", err)
	}
	if called {
		t.Fatal("expected no HTTP request without token")
	}
	if books != nil {
		t.Fatalf("books = %+v, want nil", books)
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

func TestSearchSeries_ParsesResults(t *testing.T) {
	var gotVars map[string]any
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		data := map[string]interface{}{
			"search": map[string]interface{}{
				"ids": []interface{}{123},
				"results": map[string]interface{}{
					"found": 1,
					"hits": []map[string]interface{}{
						{
							"document": map[string]interface{}{
								"id":                  123,
								"name":                "Foundation",
								"author_name":         "Isaac Asimov",
								"primary_books_count": 7,
								"readers_count":       1000,
								"books":               []string{"Foundation", "Foundation and Empire"},
							},
						},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	results, err := c.SearchSeries(context.Background(), "Foundation", 5)
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if gotVars["queryType"] != "Series" {
		t.Fatalf("queryType = %v, want Series", gotVars["queryType"])
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	got := results[0]
	if got.ForeignID != "hc-series:123" || got.ProviderID != "123" {
		t.Fatalf("ids = %q/%q, want hc-series:123/123", got.ForeignID, got.ProviderID)
	}
	if got.Title != "Foundation" || got.AuthorName != "Isaac Asimov" || got.BookCount != 7 || got.ReadersCount != 1000 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestSearchSeries_ErrorsWhenMatchesCannotBeMapped(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{
				"ids": []interface{}{123},
				"results": map[string]interface{}{
					"found": 1,
					"hits": []map[string]interface{}{
						{"document": map[string]interface{}{"name": "Dune"}},
					},
				},
			},
		}), nil
	})
	_, err := c.SearchSeries(context.Background(), "Dune", 5)
	if err == nil {
		t.Fatal("expected unmappable search response error")
	}
	if !strings.Contains(err.Error(), "no mappable series documents") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchSeries_ParsesStringResults(t *testing.T) {
	encoded := `{"found":1,"hits":[{"document":{"id":"42","name":"Murderbot Diaries","books_count":6}}]}`
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{"results": encoded},
		}), nil
	})

	results, err := c.SearchSeries(context.Background(), "Murderbot", 10)
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if len(results) != 1 || results[0].ForeignID != "hc-series:42" || results[0].BookCount != 6 {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestSearchSeries_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(strings.NewReader("bad gateway")),
			Header:     make(http.Header),
		}, nil
	})
	if _, err := c.SearchSeries(context.Background(), "anything", 10); err == nil {
		t.Fatal("expected error on 502")
	}
}

func TestGetSeriesCatalog_ParsesAndDedupesBooks(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"series_by_pk": map[string]interface{}{
				"id":          123,
				"name":        "Foundation",
				"books_count": 3,
				"author":      map[string]interface{}{"name": "Isaac Asimov"},
				"book_series": []map[string]interface{}{
					{
						"position": 2,
						"book": map[string]interface{}{
							"id":          202,
							"title":       "Foundation",
							"slug":        "foundation-later",
							"users_count": 10,
						},
					},
					{
						"position": 1,
						"book": map[string]interface{}{
							"id":          201,
							"title":       "Foundation",
							"subtitle":    "The First Book",
							"slug":        "foundation",
							"users_count": 100,
						},
					},
					{
						"position": 2,
						"book": map[string]interface{}{
							"id":    203,
							"title": "Foundation and Empire",
							"slug":  "foundation-and-empire",
						},
					},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	catalog, err := c.GetSeriesCatalog(context.Background(), "hc-series:123")
	if err != nil {
		t.Fatalf("GetSeriesCatalog: %v", err)
	}
	if catalog == nil {
		t.Fatal("expected catalog")
	}
	if catalog.ForeignID != "hc-series:123" || catalog.Title != "Foundation" || catalog.AuthorName != "Isaac Asimov" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if len(catalog.Books) != 2 {
		t.Fatalf("books len = %d, want 2: %+v", len(catalog.Books), catalog.Books)
	}
	if catalog.Books[0].ForeignID != "hc:foundation" || catalog.Books[0].Position != "1" {
		t.Fatalf("first book = %+v, want deduped lower position Foundation", catalog.Books[0])
	}
	if catalog.Books[1].ForeignID != "hc:foundation-and-empire" || catalog.Books[1].Position != "2" {
		t.Fatalf("second book = %+v", catalog.Books[1])
	}
}

func TestGetSeriesCatalog_NotFound(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"series_by_pk": nil}), nil
	})

	catalog, err := c.GetSeriesCatalog(context.Background(), "hc-series:999")
	if err != nil {
		t.Fatalf("GetSeriesCatalog: %v", err)
	}
	if catalog != nil {
		t.Fatalf("expected nil catalog, got %+v", catalog)
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

func TestGetUserLists_IncludesBuiltinShelves(t *testing.T) {
	// When me.lists returns an empty array (user has no custom lists),
	// GetUserLists must still return the 4 built-in shelf entries.
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"me":{"lists":[]}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	lists, err := c.GetUserLists(context.Background())
	if err != nil {
		t.Fatalf("GetUserLists: %v", err)
	}
	if len(lists) != len(hcBuiltinShelves) {
		t.Fatalf("want %d lists (built-ins only), got %d", len(hcBuiltinShelves), len(lists))
	}
	if lists[0].ID != -1 || lists[0].Name != "Want to Read" {
		t.Errorf("first list = %+v, want {ID:-1, Name:Want to Read}", lists[0])
	}
}

func TestGetUserLists_CustomListsAppendedAfterShelves(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"me":{"lists":[{"id":42,"name":"Favorites","slug":"favorites","books_count":7}]}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	lists, err := c.GetUserLists(context.Background())
	if err != nil {
		t.Fatalf("GetUserLists: %v", err)
	}
	want := len(hcBuiltinShelves) + 1
	if len(lists) != want {
		t.Fatalf("want %d lists, got %d", want, len(lists))
	}
	last := lists[len(lists)-1]
	if last.ID != 42 || last.Name != "Favorites" || last.BooksCount != 7 {
		t.Errorf("custom list = %+v, want {ID:42, Name:Favorites, BooksCount:7}", last)
	}
}

func TestGetListBooks_BuiltinShelfRoutesToUserBooks(t *testing.T) {
	var gotVars map[string]any
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		resp := `{"data":{"me":{"user_books":[{"book":{"id":99,"title":"Dune","slug":"dune","contributions":[{"author":{"id":1,"name":"Frank Herbert","slug":"frank-herbert"}}]}}]}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(resp)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), -1) // Want to Read
	if err != nil {
		t.Fatalf("GetListBooks shelf: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Dune" {
		t.Errorf("books = %+v, want [{Title:Dune}]", books)
	}
	if gotVars["statusID"] != float64(1) {
		t.Errorf("statusID var = %v, want 1", gotVars["statusID"])
	}
}

func TestHcShelfStatusID(t *testing.T) {
	cases := []struct{ id, want int }{{-1, 1}, {-2, 2}, {-3, 3}, {-4, 4}}
	for _, tc := range cases {
		got, ok := hcShelfStatusID(tc.id)
		if !ok || got != tc.want {
			t.Errorf("hcShelfStatusID(%d) = %d,%v, want %d,true", tc.id, got, ok, tc.want)
		}
	}
	if _, ok := hcShelfStatusID(0); ok {
		t.Error("hcShelfStatusID(0) should return false")
	}
	if _, ok := hcShelfStatusID(42); ok {
		t.Error("hcShelfStatusID(42) should return false")
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
