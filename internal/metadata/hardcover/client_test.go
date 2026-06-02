package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
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

func assertSearchRequest(t *testing.T, r *http.Request, queryType, query string) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if strings.Contains(req.Query, "_ilike") {
		t.Fatalf("search query still uses _ilike: %s", req.Query)
	}
	if !strings.Contains(req.Query, "search(query: $query") {
		t.Fatalf("search query does not use Hardcover search operation: %s", req.Query)
	}
	if got := req.Variables["queryType"]; got != queryType {
		t.Fatalf("queryType = %v, want %s", got, queryType)
	}
	if got := req.Variables["query"]; got != query {
		t.Fatalf("query = %v, want %s", got, query)
	}
	perPage, ok := req.Variables["perPage"].(float64)
	if !ok || perPage != 20 {
		t.Fatalf("perPage = %v, want 20", req.Variables["perPage"])
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
				return gqlResponse(t, http.StatusOK, map[string]interface{}{
					"search": map[string]interface{}{
						"results": map[string]interface{}{"found": 0, "hits": []interface{}{}},
					},
				}), nil
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
		return
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
		assertSearchRequest(t, r, "Author", "Sanderson")
		data := map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{
					"found": 1,
					"hits": []map[string]interface{}{
						{
							"document": map[string]interface{}{
								"id":   1,
								"name": "Brandon Sanderson",
								"slug": "brandon-sanderson",
								"bio":  "Fantasy author",
								"image": map[string]interface{}{
									"url": "https://example.com/bs.jpg",
								},
							},
						},
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

func TestSearchAuthors_ParsesStringResults(t *testing.T) {
	encoded := `{"found":1,"hits":[{"document":{"id":"80626","name":"J.K. Rowling","description":"Author","image_url":"https://example.com/jkr.jpg"}}]}`
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Author", "rowling")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{"results": encoded},
		}), nil
	})

	authors, err := c.SearchAuthors(context.Background(), "rowling")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("expected 1 author, got %d", len(authors))
	}
	if authors[0].ForeignID != "hc:80626" {
		t.Errorf("ForeignID: want 'hc:80626', got %q", authors[0].ForeignID)
	}
	if authors[0].Description != "Author" {
		t.Errorf("Description: want fallback description, got %q", authors[0].Description)
	}
	if authors[0].ImageURL != "https://example.com/jkr.jpg" {
		t.Errorf("ImageURL: got %q", authors[0].ImageURL)
	}
}

func TestSearchAuthors_Empty(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Author", "nobody")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{"found": 0, "hits": []interface{}{}},
			},
		}), nil
	})

	authors, err := c.SearchAuthors(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 0 {
		t.Errorf("expected 0 authors, got %d", len(authors))
	}
}

func TestSearchAuthors_BlankQuerySkipsAPI(t *testing.T) {
	called := false
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		called = true
		return gqlResponse(t, http.StatusOK, map[string]interface{}{}), nil
	})

	authors, err := c.SearchAuthors(context.Background(), "   ")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 0 {
		t.Fatalf("expected no authors, got %d", len(authors))
	}
	if called {
		t.Fatal("expected blank query not to call API")
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
		assertSearchRequest(t, r, "Book", "Mistborn")
		data := map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{
					"found": 1,
					"hits": []map[string]interface{}{
						{
							"document": map[string]interface{}{
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

func TestSearchBooks_ParsesStringResultsAndAuthorNames(t *testing.T) {
	encoded := `{"found":1,"hits":[{"document":{"id":"312460","title":"Dune","slug":"dune","description":"A desert planet.","release_year":"1965","rating":"4.4","ratings_count":"12000","image_url":"https://img.example.com/dune.jpg","isbns":["978-0-441-17271-9","9780441172719","0441172717"],"genres":["Science Fiction"," science fiction ","Classic"],"has_audiobook":"true","has_ebook":"true","featured_series":"Dune Chronicles","featured_series_position":"1","author_names":["Frank Herbert"]}}]}`
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Book", "dune")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{"results": encoded},
		}), nil
	})

	books, err := c.SearchBooks(context.Background(), "dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	book := books[0]
	if book.ForeignID != "hc:dune" || book.Title != "Dune" {
		t.Fatalf("unexpected book: %+v", book)
	}
	if book.ReleaseDate == nil || book.ReleaseDate.Year() != 1965 {
		t.Errorf("ReleaseDate: expected year 1965, got %v", book.ReleaseDate)
	}
	if book.Author == nil || book.Author.Name != "Frank Herbert" || book.Author.ForeignID != "" {
		t.Errorf("Author: %+v", book.Author)
	}
	if book.AverageRating != 4.4 || book.RatingsCount != 12000 {
		t.Errorf("rating/count = %f/%d, want 4.4/12000", book.AverageRating, book.RatingsCount)
	}
	if !slices.Equal(book.ISBNs, []string{"9780441172719", "0441172717"}) {
		t.Errorf("ISBNs = %+v", book.ISBNs)
	}
	if !slices.Equal(book.Genres, []string{"Science Fiction", "Classic"}) {
		t.Errorf("Genres = %+v", book.Genres)
	}
	if book.MediaType != models.MediaTypeBoth {
		t.Errorf("MediaType = %q, want %q", book.MediaType, models.MediaTypeBoth)
	}
	if len(book.SeriesRefs) != 0 {
		t.Fatalf("SeriesRefs len = %d, want 0 without Hardcover series id", len(book.SeriesRefs))
	}
}

func TestSearchBooks_ParsesSupplementalPayloadMetadata(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Book", "way of kings")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{
					"found": 1,
					"hits": []map[string]interface{}{
						{
							"document": map[string]interface{}{
								"id":                       42,
								"title":                    "The Way of Kings",
								"slug":                     "the-way-of-kings",
								"isbns":                    []interface{}{"978-0-7653-2635-5", "0765326353", "9780765326355"},
								"genres":                   []interface{}{"Fantasy", " fantasy ", ""},
								"has_audiobook":            true,
								"has_ebook":                false,
								"featured_series":          map[string]interface{}{"name": "The Stormlight Archive"},
								"featured_series_id":       103,
								"featured_series_position": 1.5,
								"author_names":             []interface{}{"", "Brandon Sanderson"},
							},
						},
					},
				},
			},
		}), nil
	})

	books, err := c.SearchBooks(context.Background(), "way of kings")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	book := books[0]
	if book.Author == nil || book.Author.Name != "Brandon Sanderson" {
		t.Fatalf("Author = %+v, want Brandon Sanderson", book.Author)
	}
	if !slices.Equal(book.ISBNs, []string{"9780765326355", "0765326353"}) {
		t.Errorf("ISBNs = %+v", book.ISBNs)
	}
	if !slices.Equal(book.Genres, []string{"Fantasy"}) {
		t.Errorf("Genres = %+v", book.Genres)
	}
	if book.MediaType != models.MediaTypeAudiobook {
		t.Errorf("MediaType = %q, want %q", book.MediaType, models.MediaTypeAudiobook)
	}
	if len(book.SeriesRefs) != 1 {
		t.Fatalf("SeriesRefs len = %d, want 1", len(book.SeriesRefs))
	}
	ref := book.SeriesRefs[0]
	if ref.ForeignID != "hc-series:103" || ref.Title != "The Stormlight Archive" || ref.Position != "1.5" || !ref.Primary {
		t.Errorf("SeriesRef = %+v", ref)
	}
}

func TestSearchBooks_DropsNonScalarGenres(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Book", "dune")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{
					"hits": []map[string]interface{}{
						{
							"document": map[string]interface{}{
								"id":    42,
								"title": "Dune",
								"slug":  "dune",
								"genres": []interface{}{
									"Science Fiction",
									true,
									map[string]interface{}{"name": "Fantasy"},
									[]interface{}{
										"Classic",
										false,
										map[string]interface{}{"name": "Space Opera"},
									},
								},
							},
						},
					},
				},
			},
		}), nil
	})

	books, err := c.SearchBooks(context.Background(), "dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if !slices.Equal(books[0].Genres, []string{"Science Fiction", "Classic"}) {
		t.Fatalf("Genres = %+v", books[0].Genres)
	}
}

func TestSearchSeriesRefsRequiresHardcoverSeriesID(t *testing.T) {
	for _, tt := range []struct {
		name        string
		seriesValue any
		idValue     any
		want        string
	}{
		{
			name:        "string title only",
			seriesValue: "Dune Chronicles",
		},
		{
			name:        "slug only",
			seriesValue: map[string]any{"name": "Dune Chronicles", "slug": "dune-chronicles"},
		},
		{
			name:        "non numeric id",
			seriesValue: "Dune Chronicles",
			idValue:     "dune-chronicles",
		},
		{
			name:        "featured series id",
			seriesValue: "Dune Chronicles",
			idValue:     "103",
			want:        "hc-series:103",
		},
		{
			name:        "map numeric id",
			seriesValue: map[string]any{"name": "Dune Chronicles", "id": float64(103), "slug": "dune-chronicles"},
			want:        "hc-series:103",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			refs := searchSeriesRefs(tt.seriesValue, tt.idValue, "1")
			if tt.want == "" {
				if len(refs) != 0 {
					t.Fatalf("SeriesRefs len = %d, want 0: %+v", len(refs), refs)
				}
				return
			}
			if len(refs) != 1 {
				t.Fatalf("SeriesRefs len = %d, want 1", len(refs))
			}
			if refs[0].ForeignID != tt.want {
				t.Fatalf("ForeignID = %q, want %q", refs[0].ForeignID, tt.want)
			}
		})
	}
}

func TestSearchBooks_SkipsUnmappableHits(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		assertSearchRequest(t, r, "Book", "Dune")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"search": map[string]interface{}{
				"results": map[string]interface{}{
					"found": 2,
					"hits": []map[string]interface{}{
						{"document": map[string]interface{}{"title": "No ID"}},
						{"document": map[string]interface{}{"id": 42, "title": "Dune"}},
					},
				},
			},
		}), nil
	})

	books, err := c.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 mappable book, got %d", len(books))
	}
	if books[0].ForeignID != "hc:42" {
		t.Errorf("ForeignID: want 'hc:42', got %q", books[0].ForeignID)
	}
}

func TestSearchBooks_BlankQuerySkipsAPI(t *testing.T) {
	called := false
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		called = true
		return gqlResponse(t, http.StatusOK, map[string]interface{}{}), nil
	})

	books, err := c.SearchBooks(context.Background(), "   ")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 0 {
		t.Fatalf("expected no books, got %d", len(books))
	}
	if called {
		t.Fatal("expected blank query not to call API")
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
		if strings.Contains(req.Query, "genres") {
			t.Fatalf("query requested removed Hardcover books.genres field: %s", req.Query)
		}
		if strings.Contains(req.Query, "cached_tags") {
			t.Fatalf("query requested Hardcover tags as genres: %s", req.Query)
		}
		for _, field := range []string{"isbns", "has_audiobook", "has_ebook", "featured_series"} {
			if strings.Contains(req.Query, field) {
				t.Fatalf("query requested search-only Hardcover field %q: %s", field, req.Query)
			}
		}
		for _, field := range []string{"default_audio_edition_id", "default_ebook_edition_id", "language"} {
			if !strings.Contains(req.Query, field) {
				t.Fatalf("query did not request Hardcover format field %q: %s", field, req.Query)
			}
		}
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
					"audio_seconds": 7200,
					"language":      map[string]interface{}{"language": "en"},
					"contributions": []map[string]interface{}{
						{"author": map[string]interface{}{"id": 1, "name": "Frank Herbert", "slug": "frank-herbert"}},
					},
				},
				{
					"id":            11,
					"title":         "Dune (Spanish edition)",
					"slug":          "dune-es",
					"release_year":  1965,
					"ratings_count": 50,
					"rating":        4.2,
					"users_count":   100,
					"language":      map[string]interface{}{"language": "es"},
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
	if len(books) != 2 {
		t.Fatalf("books len = %d, want 2", len(books))
	}
	book := books[0]
	if book.ForeignID != "hc:dune" || book.Title != "Dune" || book.ImageURL == "" {
		t.Fatalf("unexpected book: %+v", book)
	}
	if book.DurationSeconds != 7200 {
		t.Fatalf("DurationSeconds = %d, want 7200", book.DurationSeconds)
	}
	if len(book.Genres) != 0 {
		t.Fatalf("Genres = %+v", book.Genres)
	}
	if book.MediaType != "" {
		t.Fatalf("MediaType = %q, want empty author import default", book.MediaType)
	}
	// #889: Language must be propagated so the aggregator can drop foreign
	// editions via the metadata profile's allowed_languages filter. Before
	// this fix every Hardcover-sourced supplemental book arrived with
	// Language="" and slipped past the filter as "unknown language: pass".
	if book.Language != "eng" {
		t.Errorf("English book Language = %q, want %q (normalized to ISO 639-2)", book.Language, "eng")
	}
	if books[1].Language != "spa" {
		t.Errorf("Spanish book Language = %q, want %q (normalized to ISO 639-2)", books[1].Language, "spa")
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
		return
	}
	if author.Name != "Neil Gaiman" {
		t.Errorf("Name: want 'Neil Gaiman', got %q", author.Name)
	}
	// ForeignID strips "hc:" for the query but toAuthor re-adds it
	if author.ForeignID != "hc:neil-gaiman" {
		t.Errorf("ForeignID: want 'hc:neil-gaiman', got %q", author.ForeignID)
	}
}

func TestGetAuthor_NumericID(t *testing.T) {
	var gotVars map[string]any
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		gotVars = req.Variables
		data := map[string]interface{}{
			"authors": []map[string]interface{}{
				{"id": 5, "name": "Neil Gaiman", "slug": "neil-gaiman", "bio": "Writer", "image": nil},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	author, err := c.GetAuthor(context.Background(), "hc:5")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author == nil || author.ForeignID != "hc:neil-gaiman" {
		t.Fatalf("author = %+v, want slug-backed author", author)
	}
	if gotVars["id"] != float64(5) && gotVars["id"] != 5 {
		t.Fatalf("id variable = %#v, want 5", gotVars["id"])
	}
	if _, ok := gotVars["slug"]; ok {
		t.Fatalf("slug variable present for numeric lookup: %#v", gotVars["slug"])
	}
	if !strings.Contains(gotQuery, "id: {_eq: $id}") {
		t.Fatalf("query did not use id lookup: %s", gotQuery)
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
		return
	}
	if book.Title != "American Gods" {
		t.Errorf("Title: want 'American Gods', got %q", book.Title)
	}
	if book.ForeignID != "hc:american-gods" {
		t.Errorf("ForeignID: want 'hc:american-gods', got %q", book.ForeignID)
	}
}

func TestGetBook_NumericID(t *testing.T) {
	var gotVars map[string]any
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		gotVars = req.Variables
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

	book, err := c.GetBook(context.Background(), "hc:99")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book == nil || book.ForeignID != "hc:american-gods" {
		t.Fatalf("book = %+v, want slug-backed book", book)
	}
	if gotVars["id"] != float64(99) && gotVars["id"] != 99 {
		t.Fatalf("id variable = %#v, want 99", gotVars["id"])
	}
	if _, ok := gotVars["slug"]; ok {
		t.Fatalf("slug variable present for numeric lookup: %#v", gotVars["slug"])
	}
	if !strings.Contains(gotQuery, "id: {_eq: $id}") {
		t.Fatalf("query did not use id lookup: %s", gotQuery)
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

func TestGetEditions_MapsGraphQLResponse(t *testing.T) {
	var gotQuery string
	var gotVars map[string]any
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotQuery = req.Query
		gotVars = req.Variables
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					"id":                  123,
					"title":               "Dune Deluxe",
					"isbn_10":             "0441172717",
					"isbn_13":             "9780441172719",
					"asin":                "B0036S4B2G",
					"publisher":           map[string]interface{}{"name": "Ace Books"},
					"release_date":        "1965-08-01",
					"release_year":        1965,
					"physical_format":     "Hardcover",
					"edition_format":      "Anniversary Edition",
					"edition_information": "50th anniversary",
					"pages":               412,
					"image":               map[string]interface{}{"url": "https://img.example/dune.jpg"},
					"language":            map[string]interface{}{"language": "English"},
					"reading_format":      map[string]interface{}{"format": "Physical"},
					"book":                map[string]interface{}{"title": "Dune"},
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	editions, err := c.GetEditions(context.Background(), "hc:dune")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != 1 {
		t.Fatalf("expected 1 edition, got %d", len(editions))
	}
	if strings.Contains(gotQuery, "iso_639_1") {
		t.Fatalf("GetEditions query uses unsupported language field: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "book: {slug: {_eq: $slug}}") {
		t.Fatalf("GetEditions slug query did not filter by book slug: %s", gotQuery)
	}
	if gotVars["slug"] != "dune" {
		t.Fatalf("slug variable = %#v, want dune", gotVars["slug"])
	}
	if gotVars["limit"] != float64(editionsPageSize) {
		t.Fatalf("limit variable = %#v, want %d", gotVars["limit"], editionsPageSize)
	}
	if gotVars["offset"] != float64(0) {
		t.Fatalf("offset variable = %#v, want 0", gotVars["offset"])
	}
	e := editions[0]
	if e.ForeignID != "hc:123" {
		t.Errorf("ForeignID = %q, want hc:123", e.ForeignID)
	}
	if e.Title != "Dune Deluxe" {
		t.Errorf("Title = %q, want Dune Deluxe", e.Title)
	}
	if e.ISBN10 == nil || *e.ISBN10 != "0441172717" {
		t.Errorf("ISBN10 = %v, want 0441172717", e.ISBN10)
	}
	if e.ISBN13 == nil || *e.ISBN13 != "9780441172719" {
		t.Errorf("ISBN13 = %v, want 9780441172719", e.ISBN13)
	}
	if e.ASIN == nil || *e.ASIN != "B0036S4B2G" {
		t.Errorf("ASIN = %v, want B0036S4B2G", e.ASIN)
	}
	if e.Publisher != "Ace Books" {
		t.Errorf("Publisher = %q, want Ace Books", e.Publisher)
	}
	if e.PublishDate == nil || e.PublishDate.Format("2006-01-02") != "1965-08-01" {
		t.Errorf("PublishDate = %v, want 1965-08-01", e.PublishDate)
	}
	if e.Format != "Hardcover" {
		t.Errorf("Format = %q, want Hardcover", e.Format)
	}
	if e.NumPages == nil || *e.NumPages != 412 {
		t.Errorf("NumPages = %v, want 412", e.NumPages)
	}
	if e.ImageURL != "https://img.example/dune.jpg" {
		t.Errorf("ImageURL = %q, want cover URL", e.ImageURL)
	}
	if e.Language != "eng" {
		t.Errorf("Language = %q, want eng", e.Language)
	}
	if e.EditionInfo != "50th anniversary" {
		t.Errorf("EditionInfo = %q, want 50th anniversary", e.EditionInfo)
	}
	if !e.Monitored {
		t.Error("Monitored = false, want true")
	}
	if e.IsEbook {
		t.Error("IsEbook = true, want false")
	}
}

func TestGetEditions_FormatFallbacks(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					"id":             124,
					"title":          "",
					"release_year":   2021,
					"edition_format": "Kindle Edition",
					"reading_format": map[string]interface{}{"format": "Ebook"},
					"book":           map[string]interface{}{"title": "Dune"},
				},
				{
					"id":             125,
					"title":          "Dune Audio",
					"reading_format": map[string]interface{}{"format": "Audiobook"},
					"audio_seconds":  3600,
				},
			},
		}
		return gqlResponse(t, http.StatusOK, data), nil
	})

	editions, err := c.GetEditions(context.Background(), "hc:dune")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != 2 {
		t.Fatalf("expected 2 editions, got %d", len(editions))
	}
	ebook := editions[0]
	if ebook.Title != "Dune" {
		t.Errorf("fallback title = %q, want Dune", ebook.Title)
	}
	if ebook.Format != "Kindle Edition" {
		t.Errorf("ebook Format = %q, want Kindle Edition", ebook.Format)
	}
	if !ebook.IsEbook {
		t.Error("Kindle edition should be marked as ebook")
	}
	if ebook.PublishDate == nil || ebook.PublishDate.Format("2006-01-02") != "2021-01-01" {
		t.Errorf("PublishDate = %v, want 2021-01-01", ebook.PublishDate)
	}
	audio := editions[1]
	if audio.Format != "Audiobook" {
		t.Errorf("audio Format = %q, want Audiobook", audio.Format)
	}
	if audio.IsEbook {
		t.Error("audiobook should not be marked as ebook")
	}
}

func TestGetEditions_NumericIDUsesBookID(t *testing.T) {
	var gotQuery string
	var gotVars map[string]any
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotQuery = req.Query
		gotVars = req.Variables
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"editions": []interface{}{}}), nil
	})

	editions, err := c.GetEditions(context.Background(), "hc:312460")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != 0 {
		t.Fatalf("expected no editions, got %d", len(editions))
	}
	if gotVars["bookID"] != float64(312460) {
		t.Fatalf("bookID variable = %#v, want 312460", gotVars["bookID"])
	}
	if _, ok := gotVars["slug"]; ok {
		t.Fatalf("slug variable present for numeric lookup: %#v", gotVars["slug"])
	}
	if !strings.Contains(gotQuery, "book_id: {_eq: $bookID}") {
		t.Fatalf("query did not use book_id lookup: %s", gotQuery)
	}
	if strings.Contains(gotQuery, "iso_639_1") {
		t.Fatalf("GetEditions query uses unsupported language field: %s", gotQuery)
	}
}

func TestGetEditions_Paginates(t *testing.T) {
	var offsets []int
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		offset := int(req.Variables["offset"].(float64))
		offsets = append(offsets, offset)
		count := editionsPageSize
		if offset > 0 {
			count = 1
		}
		editions := make([]map[string]interface{}, 0, count)
		for i := 0; i < count; i++ {
			editions = append(editions, map[string]interface{}{
				"id":    offset + i + 1,
				"title": "Edition",
			})
		}
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"editions": editions}), nil
	})

	editions, err := c.GetEditions(context.Background(), "hc:dune")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != editionsPageSize+1 {
		t.Fatalf("edition count = %d, want %d", len(editions), editionsPageSize+1)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != editionsPageSize {
		t.Fatalf("offsets = %v, want [0 %d]", offsets, editionsPageSize)
	}
}

func TestGetEditions_GraphQLError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, `{"errors":[{"message":"field not found"}]}`), nil
	})

	_, err := c.GetEditions(context.Background(), "hc:dune")
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "hardcover get editions") {
		t.Fatalf("error = %v, want hardcover get editions wrapper", err)
	}
}

func TestGetEditions_HTTPError(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("forbidden")),
			Header:     make(http.Header),
		}, nil
	})

	_, err := c.GetEditions(context.Background(), "hc:dune")
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "hardcover get editions") {
		t.Fatalf("error = %v, want hardcover get editions wrapper", err)
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
		return
	}
	if book.Title != "The Name of the Wind" {
		t.Errorf("Title: want 'The Name of the Wind', got %q", book.Title)
	}
}

func TestGetBookByISBN_WithLanguage(t *testing.T) {
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req gqlRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotQuery = req.Query
		data := map[string]interface{}{
			"editions": []map[string]interface{}{
				{
					"language": map[string]interface{}{"language": "German"},
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
		return
	}
	if strings.Contains(gotQuery, "iso_639_1") {
		t.Fatalf("GetBookByISBN query uses unsupported language field: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "language { language }") {
		t.Fatalf("GetBookByISBN query did not request language field: %s", gotQuery)
	}
	if book.Language != "ger" {
		t.Errorf("Language: want 'ger', got %q", book.Language)
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
		return
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
		return
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
		return
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

func TestToBook_DefaultEditionIDsSetMediaType(t *testing.T) {
	c := New()
	audioID := 10
	ebookID := 20
	cases := []struct {
		name string
		book hcBook
		want string
	}{
		{
			name: "both",
			book: hcBook{
				DefaultAudioEditionID: &audioID,
				DefaultEbookEditionID: &ebookID,
			},
			want: models.MediaTypeBoth,
		},
		{
			name: "audiobook",
			book: hcBook{
				DefaultAudioEditionID: &audioID,
			},
			want: models.MediaTypeAudiobook,
		},
		{
			name: "ebook",
			book: hcBook{
				DefaultEbookEditionID: &ebookID,
			},
			want: models.MediaTypeEbook,
		},
		{
			name: "neither",
			book: hcBook{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.book.ID = 1
			tc.book.Title = "Format Test"
			tc.book.Slug = "format-test"
			got := c.toBook(tc.book)
			if got.MediaType != tc.want {
				t.Fatalf("MediaType = %q, want %q", got.MediaType, tc.want)
			}
		})
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

func TestGetUserWishlist_UsesTokenSource(t *testing.T) {
	var gotAuth string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"me": []map[string]interface{}{{"user_books": []interface{}{}}},
		}), nil
	}).WithTokenSource(func(context.Context) string {
		return "Bearer source-token"
	})

	candidates, err := c.GetUserWishlist(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetUserWishlist: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected empty candidates, got %d", len(candidates))
	}
	if gotAuth != "Bearer source-token" {
		t.Fatalf("Authorization = %q, want Bearer source-token", gotAuth)
	}
}

func TestGetUserWishlist_Success(t *testing.T) {
	var gotAuth string
	year := 2019
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		data := map[string]interface{}{
			"me": []map[string]interface{}{
				{
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
			"me": []map[string]interface{}{{"user_books": []interface{}{}}},
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
	if v, ok := gotVars["statusID"].(float64); !ok || v != hcStatusWantToRead {
		t.Errorf("statusID variable: want %d, got %v", hcStatusWantToRead, gotVars["statusID"])
	}
}

func TestGetUserWishlist_Empty(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"me": []map[string]interface{}{{"user_books": []interface{}{}}},
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
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req gqlRequest
		_ = json.Unmarshal(bodyBytes, &req)
		gotQuery = req.Query
		body := `{"data":{"me":[{
			"want_to_read":{"aggregate":{"count":11}},
			"currently_reading":{"aggregate":{"count":2}},
			"read":{"aggregate":{"count":30}},
			"did_not_finish":{"aggregate":{"count":4}},
			"lists":[]
		}]}}`
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
	want := []HCList{
		{ID: -1, Name: "Want to Read", Slug: "want-to-read", BooksCount: 11},
		{ID: -2, Name: "Currently Reading", Slug: "currently-reading", BooksCount: 2},
		{ID: -3, Name: "Read", Slug: "read", BooksCount: 30},
		{ID: -4, Name: "Did Not Finish", Slug: "did-not-finish", BooksCount: 4},
	}
	for i, wantShelf := range want {
		if lists[i] != wantShelf {
			t.Errorf("list[%d] = %+v, want %+v", i, lists[i], wantShelf)
		}
	}
	for _, wantFragment := range []string{
		"want_to_read: user_books_aggregate",
		"currently_reading: user_books_aggregate",
		"read: user_books_aggregate",
		"did_not_finish: user_books_aggregate",
		"aggregate { count }",
	} {
		if !strings.Contains(gotQuery, wantFragment) {
			t.Errorf("query missing %q: %s", wantFragment, gotQuery)
		}
	}
}

func TestGetUserLists_CustomListsAppendedAfterShelves(t *testing.T) {
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"me":[{"lists":[{"id":42,"name":"Favorites","slug":"favorites","books_count":7}]}]}}`
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
	cases := []struct {
		name         string
		listID       int
		wantStatusID int
	}{
		{name: "want to read", listID: -1, wantStatusID: 1},
		{name: "currently reading", listID: -2, wantStatusID: 2},
		{name: "read", listID: -3, wantStatusID: 3},
		{name: "did not finish", listID: -4, wantStatusID: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotVars map[string]any
			c := newMockClient(func(r *http.Request) (*http.Response, error) {
				var req gqlRequest
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &req)
				gotVars = req.Variables
				resp := `{"data":{"me":[{"user_books":[{"book":{"id":99,"title":"Dune","slug":"dune","contributions":[{"author":{"id":1,"name":"Frank Herbert","slug":"frank-herbert"}}]}}]}]}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(resp)),
					Header:     make(http.Header),
				}, nil
			})
			c = c.WithToken("hc-token")

			books, err := c.GetListBooks(context.Background(), tc.listID)
			if err != nil {
				t.Fatalf("GetListBooks shelf: %v", err)
			}
			if len(books) != 1 || books[0].Title != "Dune" {
				t.Errorf("books = %+v, want [{Title:Dune}]", books)
			}
			wantStatusID := float64(tc.wantStatusID)
			if gotVars["statusID"] != wantStatusID {
				t.Errorf("statusID var = %v, want %v", gotVars["statusID"], wantStatusID)
			}
			if gotVars["limit"] != float64(listBooksPageSize) {
				t.Errorf("limit var = %v, want %d", gotVars["limit"], listBooksPageSize)
			}
			if gotVars["offset"] != float64(0) {
				t.Errorf("offset var = %v, want 0", gotVars["offset"])
			}
		})
	}
}

func TestGetListBooks_BuiltinShelfPaginates(t *testing.T) {
	var offsets []int
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		offset := int(req.Variables["offset"].(float64))
		offsets = append(offsets, offset)
		count := listBooksPageSize
		if offset > 0 {
			count = 2
		}
		userBooks := make([]map[string]interface{}, 0, count)
		for i := 0; i < count; i++ {
			bookID := offset + i + 1
			userBooks = append(userBooks, map[string]interface{}{
				"book": map[string]interface{}{
					"id":    bookID,
					"title": "Paged Shelf Book",
					"slug":  "paged-shelf-book",
					"contributions": []map[string]interface{}{
						{"author": map[string]interface{}{"id": 1, "name": "Author", "slug": "author"}},
					},
				},
			})
		}
		return gqlResponse(t, http.StatusOK, map[string]interface{}{
			"me": []map[string]interface{}{{"user_books": userBooks}},
		}), nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), -1)
	if err != nil {
		t.Fatalf("GetListBooks shelf: %v", err)
	}
	if len(books) != listBooksPageSize+2 {
		t.Fatalf("book count = %d, want %d", len(books), listBooksPageSize+2)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != listBooksPageSize {
		t.Fatalf("offsets = %v, want [0 %d]", offsets, listBooksPageSize)
	}
}

func TestGetListBooks_PositiveID(t *testing.T) {
	var gotVars map[string]any
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		gotQuery = req.Query
		resp := `{"data":{"list_books":[{"book":{"id":99,"title":"Dune","slug":"dune","contributions":[{"author":{"id":1,"name":"Frank Herbert","slug":"frank-herbert"}}]}}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(resp)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetListBooks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Dune" {
		t.Errorf("books = %+v, want [{Title:Dune}]", books)
	}
	if gotVars["id"] != float64(42) {
		t.Errorf("id var = %v, want 42", gotVars["id"])
	}
	if gotVars["limit"] != float64(listBooksPageSize) {
		t.Errorf("limit var = %v, want %d", gotVars["limit"], listBooksPageSize)
	}
	if gotVars["offset"] != float64(0) {
		t.Errorf("offset var = %v, want 0", gotVars["offset"])
	}
	if !strings.Contains(gotQuery, "list_books(") {
		t.Errorf("query should use root list_books field, got: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "list_id: {_eq: $id}") {
		t.Errorf("query should scope by list_id, got: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "limit: $limit") || !strings.Contains(gotQuery, "offset: $offset") {
		t.Errorf("query should include limit and offset, got: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "order_by: [{position: asc}, {id: asc}]") {
		t.Errorf("query should order by position and id, got: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "lists(where:") {
		t.Errorf("query should not use nested lists field, got: %q", gotQuery)
	}
}

func TestGetListBooks_PositiveIDPaginates(t *testing.T) {
	var offsets []int
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		offset := int(req.Variables["offset"].(float64))
		offsets = append(offsets, offset)
		count := listBooksPageSize
		if offset > 0 {
			count = 1
		}
		listBooks := make([]map[string]interface{}, 0, count)
		for i := 0; i < count; i++ {
			bookID := offset + i + 1
			listBooks = append(listBooks, map[string]interface{}{
				"book": map[string]interface{}{
					"id":    bookID,
					"title": "Paged Book",
					"slug":  "paged-book",
					"contributions": []map[string]interface{}{
						{"author": map[string]interface{}{"id": 1, "name": "Author", "slug": "author"}},
					},
				},
			})
		}
		return gqlResponse(t, http.StatusOK, map[string]interface{}{"list_books": listBooks}), nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetListBooks: %v", err)
	}
	if len(books) != listBooksPageSize+1 {
		t.Fatalf("book count = %d, want %d", len(books), listBooksPageSize+1)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != listBooksPageSize {
		t.Fatalf("offsets = %v, want [0 %d]", offsets, listBooksPageSize)
	}
}

// TestGetListBooks_PositiveIDPopulatesSeriesRefs covers issue #805: the
// list_books GraphQL query now asks for featured_series and the parsed book
// carries those forward as SeriesRefs so the list syncer can persist them.
func TestGetListBooks_PositiveIDPopulatesSeriesRefs(t *testing.T) {
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		resp := `{"data":{"list_books":[{"book":{"id":99,"title":"Dune","slug":"dune","featured_series":{"id":17,"name":"Dune Chronicles"},"featured_series_id":17,"featured_series_position":1,"contributions":[{"author":{"id":1,"name":"Frank Herbert","slug":"frank-herbert"}}]}}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(resp)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetListBooks: %v", err)
	}
	if !strings.Contains(gotQuery, "featured_series { id name }") {
		t.Errorf("query missing featured_series selection: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "featured_series_position") {
		t.Errorf("query missing featured_series_position: %q", gotQuery)
	}
	if len(books) != 1 {
		t.Fatalf("books len = %d, want 1", len(books))
	}
	if len(books[0].SeriesRefs) != 1 {
		t.Fatalf("SeriesRefs len = %d, want 1: %+v", len(books[0].SeriesRefs), books[0].SeriesRefs)
	}
	ref := books[0].SeriesRefs[0]
	if ref.ForeignID != "hc-series:17" || ref.Title != "Dune Chronicles" || ref.Position != "1" || !ref.Primary {
		t.Errorf("SeriesRef = %+v, want hc-series:17/Dune Chronicles/1/primary", ref)
	}
}

// TestGetListBooks_BuiltinShelfPopulatesSeriesRefs mirrors the above for the
// shelf code path (negative listIDs use the me.user_books query).
func TestGetListBooks_BuiltinShelfPopulatesSeriesRefs(t *testing.T) {
	var gotQuery string
	c := newMockClient(func(r *http.Request) (*http.Response, error) {
		var req gqlRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		resp := `{"data":{"me":[{"user_books":[{"book":{"id":99,"title":"The Way of Kings","slug":"the-way-of-kings","featured_series":{"id":103,"name":"The Stormlight Archive"},"featured_series_id":103,"featured_series_position":1,"contributions":[{"author":{"id":2,"name":"Brandon Sanderson","slug":"brandon-sanderson"}}]}}]}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(resp)),
			Header:     make(http.Header),
		}, nil
	})
	c = c.WithToken("hc-token")

	books, err := c.GetListBooks(context.Background(), -1)
	if err != nil {
		t.Fatalf("GetListBooks shelf: %v", err)
	}
	if !strings.Contains(gotQuery, "featured_series { id name }") {
		t.Errorf("shelf query missing featured_series selection: %q", gotQuery)
	}
	if len(books) != 1 || len(books[0].SeriesRefs) != 1 {
		t.Fatalf("books = %+v", books)
	}
	ref := books[0].SeriesRefs[0]
	if ref.ForeignID != "hc-series:103" || ref.Title != "The Stormlight Archive" || ref.Position != "1" || !ref.Primary {
		t.Errorf("shelf SeriesRef = %+v", ref)
	}
}

// TestFeaturedSeriesRefs_FallbackAndEdgeCases exercises featuredSeriesRefs
// directly so the conversion rules don't drift silently.
func TestFeaturedSeriesRefs_FallbackAndEdgeCases(t *testing.T) {
	// Title from relation, id falls back to scalar featured_series_id.
	scalarID := 55
	refs := featuredSeriesRefs(&hcFeaturedSeries{ID: 0, Name: "Foundation"}, &scalarID, 2)
	if len(refs) != 1 || refs[0].ForeignID != "hc-series:55" || refs[0].Title != "Foundation" || refs[0].Position != "2" {
		t.Errorf("scalar id fallback: %+v", refs)
	}

	// Missing id everywhere → drop the ref (we cannot link without a stable
	// foreign id).
	if got := featuredSeriesRefs(&hcFeaturedSeries{Name: "Anon"}, nil, nil); got != nil {
		t.Errorf("missing id should drop ref, got %+v", got)
	}

	// Nil relation, but scalar id alone (no title) → drop: a series with no
	// name is unusable upstream.
	zero := 0
	if got := featuredSeriesRefs(nil, &zero, nil); got != nil {
		t.Errorf("zero id should drop ref, got %+v", got)
	}
}

func TestHcShelfStatusID(t *testing.T) {
	cases := []struct {
		name string
		id   int
		want int
	}{
		{name: "want to read", id: -1, want: 1},
		{name: "currently reading", id: -2, want: 2},
		{name: "read", id: -3, want: 3},
		{name: "did not finish", id: -4, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hcShelfStatusID(tc.id)
			if !ok || got != tc.want {
				t.Errorf("hcShelfStatusID(%d) = %d,%v, want %d,true", tc.id, got, ok, tc.want)
			}
		})
	}
	if _, ok := hcShelfStatusID(0); ok {
		t.Error("hcShelfStatusID(0) should return false")
	}
	if _, ok := hcShelfStatusID(42); ok {
		t.Error("hcShelfStatusID(42) should return false")
	}
	if _, ok := hcShelfStatusID(-5); ok {
		t.Error("hcShelfStatusID(-5) should return false")
	}
}

func TestHardcoverLanguageName_NormalizesNamesAndCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *hcLanguage
		want string
	}{
		{name: "nil", in: nil, want: ""},
		{name: "blank", in: &hcLanguage{Language: " "}, want: ""},
		{name: "english name", in: &hcLanguage{Language: "English"}, want: "eng"},
		{name: "german name", in: &hcLanguage{Language: "German"}, want: "ger"},
		{name: "two letter code", in: &hcLanguage{Language: "de"}, want: "ger"},
		{name: "three letter code", in: &hcLanguage{Language: "fre"}, want: "fre"},
		{name: "unknown", in: &hcLanguage{Language: "Klingon"}, want: "klingon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hardcoverLanguageName(tc.in); got != tc.want {
				t.Errorf("hardcoverLanguageName(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
