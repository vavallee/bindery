package openlibrary

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// pathTransport routes HTTP calls by URL path to a handler map.
// Any path not in the map returns 404.
type pathTransport struct {
	t        *testing.T
	handlers map[string]interface{} // path → string body or func(*http.Request)string
	status   map[string]int         // optional override status per path
}

func (pt *pathTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	status := http.StatusOK
	if s, ok := pt.status[r.URL.Path]; ok {
		status = s
	}

	var body string
	if h, ok := pt.handlers[r.URL.Path]; ok {
		switch v := h.(type) {
		case string:
			body = v
		case func(*http.Request) string:
			body = v(r)
		}
	} else {
		status = http.StatusNotFound
		body = `{"error":"not found"}`
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func newClientWithPaths(t *testing.T, handlers map[string]interface{}) *Client {
	t.Helper()
	return &Client{
		http: &http.Client{
			Transport: &pathTransport{t: t, handlers: handlers, status: map[string]int{}},
		},
	}
}

func newClientWithStatus(t *testing.T, handlers map[string]interface{}, status map[string]int) *Client {
	t.Helper()
	return &Client{
		http: &http.Client{
			Transport: &pathTransport{t: t, handlers: handlers, status: status},
		},
	}
}

func jsonStr(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// --- SearchAuthors ---

func TestSearchAuthors_HTTP(t *testing.T) {
	resp := authorSearchResponse{
		NumFound: 1,
		Docs: []authorSearchDoc{
			{
				Key:          "OL123A",
				Name:         "Frank Herbert",
				TopWork:      "Dune",
				WorkCount:    20,
				RatingsAvg:   4.5,
				RatingsCount: 1000,
			},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/search/authors.json": jsonStr(resp),
	})

	authors, err := c.SearchAuthors(context.Background(), "Frank Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("expected 1 author, got %d", len(authors))
	}
	a := authors[0]
	if a.ForeignID != "OL123A" {
		t.Errorf("ForeignID: want 'OL123A', got %q", a.ForeignID)
	}
	if a.Name != "Frank Herbert" {
		t.Errorf("Name: want 'Frank Herbert', got %q", a.Name)
	}
	if a.AverageRating != 4.5 {
		t.Errorf("AverageRating: want 4.5, got %f", a.AverageRating)
	}
	if a.Statistics == nil || a.Statistics.BookCount != 20 {
		t.Errorf("Statistics.BookCount: want 20, got %v", a.Statistics)
	}
}

func TestSearchAuthors_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/search/authors.json": "server error"},
		map[string]int{"/search/authors.json": http.StatusInternalServerError},
	)
	_, err := c.SearchAuthors(context.Background(), "anyone")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSearchAuthors_HTTP_Empty(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/search/authors.json": jsonStr(authorSearchResponse{}),
	})
	authors, err := c.SearchAuthors(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 0 {
		t.Errorf("expected 0 authors, got %d", len(authors))
	}
}

// --- SearchBooks ---

func TestSearchBooks_HTTP(t *testing.T) {
	coverI := 12345
	resp := searchResponse{
		NumFound: 1,
		Docs: []searchDoc{
			{
				Key:              "/works/OL456W",
				Title:            "Dune",
				AuthorName:       []string{"Frank Herbert"},
				AuthorKey:        []string{"OL123A"},
				FirstPublishYear: 1965,
				CoverI:           &coverI,
				Subject:          []string{"Science Fiction", "Adventure"},
				RatingsCount:     2500,
			},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json": jsonStr(resp),
	})

	books, err := c.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	b := books[0]
	if b.ForeignID != "OL456W" {
		t.Errorf("ForeignID: want 'OL456W', got %q", b.ForeignID)
	}
	if b.Title != "Dune" {
		t.Errorf("Title: want 'Dune', got %q", b.Title)
	}
	if b.Author == nil || b.Author.Name != "Frank Herbert" {
		t.Errorf("Author: %+v", b.Author)
	}
	if b.Author.ForeignID != "OL123A" {
		t.Errorf("Author.ForeignID: want 'OL123A', got %q", b.Author.ForeignID)
	}
	if b.ReleaseDate == nil || b.ReleaseDate.Year() != 1965 {
		t.Errorf("ReleaseDate year: expected 1965")
	}
	if !strings.Contains(b.ImageURL, "12345") {
		t.Errorf("ImageURL should contain cover ID 12345, got %q", b.ImageURL)
	}
	if b.RatingsCount != 2500 {
		t.Errorf("RatingsCount: want 2500, got %d", b.RatingsCount)
	}
}

func TestSearchBooks_HTTP_NoAuthorKey(t *testing.T) {
	resp := searchResponse{
		Docs: []searchDoc{
			{
				Key:        "/works/OL789W",
				Title:      "No Key Book",
				AuthorName: []string{"Some Author"},
				// No AuthorKey
			},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json": jsonStr(resp),
	})

	books, err := c.SearchBooks(context.Background(), "test")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if books[0].Author == nil {
		t.Fatal("expected Author to be set")
	}
	if books[0].Author.ForeignID != "" {
		t.Errorf("expected empty ForeignID when no AuthorKey, got %q", books[0].Author.ForeignID)
	}
}

func TestSearchBooks_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/search.json": "error"},
		map[string]int{"/search.json": http.StatusInternalServerError},
	)
	_, err := c.SearchBooks(context.Background(), "dune")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

// --- GetAuthor ---

func TestGetAuthor_HTTP(t *testing.T) {
	resp := authorResponse{
		Key:    "/authors/OL123A",
		Name:   "Frank Herbert",
		Photos: []int{98765},
	}
	resp.Bio = "American science fiction author"

	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A.json": jsonStr(resp),
	})

	author, err := c.GetAuthor(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author.Name != "Frank Herbert" {
		t.Errorf("Name: want 'Frank Herbert', got %q", author.Name)
	}
	if author.ForeignID != "OL123A" {
		t.Errorf("ForeignID: want 'OL123A', got %q", author.ForeignID)
	}
	if !strings.Contains(author.ImageURL, "98765") {
		t.Errorf("ImageURL should contain photo ID 98765, got %q", author.ImageURL)
	}
}

func TestGetAuthor_HTTP_UsePersonalName(t *testing.T) {
	resp := authorResponse{
		Key:          "/authors/OL999A",
		Name:         "",
		PersonalName: "Personal Name Fallback",
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL999A.json": jsonStr(resp),
	})

	author, err := c.GetAuthor(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author.Name != "Personal Name Fallback" {
		t.Errorf("Name: want 'Personal Name Fallback', got %q", author.Name)
	}
}

func TestGetAuthor_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/authors/OL404A.json": "not found"},
		map[string]int{"/authors/OL404A.json": http.StatusNotFound},
	)
	_, err := c.GetAuthor(context.Background(), "OL404A")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestGetAuthor_HTTP_NegativePhoto(t *testing.T) {
	resp := authorResponse{
		Key:    "/authors/OL888A",
		Name:   "No Photo Author",
		Photos: []int{-1}, // OL uses -1 for "no photo"
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL888A.json": jsonStr(resp),
	})

	author, err := c.GetAuthor(context.Background(), "OL888A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if author.ImageURL != "" {
		t.Errorf("expected empty ImageURL for negative photo ID, got %q", author.ImageURL)
	}
}

// TestGetAuthor_HTTP_BioShapesAndPhoto pins the author-profile parsing the
// refresh path depends on (Discussion #1226): the OpenLibrary bio field arrives
// as either a bare string or a {type,value} object, and the photo arrives as a
// numeric cover id that must become a covers.openlibrary.org image URL. Both bio
// shapes must populate Description so a Refresh Metadata can persist them.
func TestGetAuthor_HTTP_BioShapesAndPhoto(t *testing.T) {
	cases := []struct {
		name string
		bio  interface{}
	}{
		{name: "string bio", bio: "British writer of novels, comics and TV."},
		{name: "object bio", bio: map[string]interface{}{
			"type":  "/type/text",
			"value": "British writer of novels, comics and TV.",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := authorResponse{
				Key:    "/authors/OL6094856A",
				Name:   "Paul Cornell",
				Bio:    tc.bio,
				Photos: []int{14431281},
			}
			c := newClientWithPaths(t, map[string]interface{}{
				"/authors/OL6094856A.json": jsonStr(resp),
			})

			author, err := c.GetAuthor(context.Background(), "OL6094856A")
			if err != nil {
				t.Fatalf("GetAuthor: %v", err)
			}
			if author.Description != "British writer of novels, comics and TV." {
				t.Errorf("Description: want bio text, got %q", author.Description)
			}
			if author.ImageURL != "https://covers.openlibrary.org/a/id/14431281-L.jpg" {
				t.Errorf("ImageURL: want photo cover URL, got %q", author.ImageURL)
			}
		})
	}
}

// --- GetEditions ---

func TestGetEditions_HTTP(t *testing.T) {
	langKey := struct{ Key string }{Key: "/languages/eng"}
	resp := editionsResponse{
		Entries: []editionEntry{
			{
				Key:            "/books/OL001M",
				Title:          "Dune (Paperback)",
				Publishers:     []string{"Ace Books"},
				ISBN13:         []string{"9780441013593"},
				ISBN10:         []string{"0441013597"},
				PhysicalFormat: "Paperback",
				NumberOfPages:  412,
				Languages: []struct {
					Key string `json:"key"`
				}{{Key: "/languages/eng"}},
				Covers: []int{54321},
			},
		},
	}
	_ = langKey

	handlers := map[string]interface{}{
		"/works/OL456W/editions.json": jsonStr(resp),
	}
	c := newClientWithPaths(t, handlers)

	editions, err := c.GetEditions(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != 1 {
		t.Fatalf("expected 1 edition, got %d", len(editions))
	}
	e := editions[0]
	if e.ForeignID != "OL001M" {
		t.Errorf("ForeignID: want 'OL001M', got %q", e.ForeignID)
	}
	if e.Publisher != "Ace Books" {
		t.Errorf("Publisher: want 'Ace Books', got %q", e.Publisher)
	}
	if e.ISBN13 == nil || *e.ISBN13 != "9780441013593" {
		t.Errorf("ISBN13: want '9780441013593', got %v", e.ISBN13)
	}
	if e.Language != "eng" {
		t.Errorf("Language: want 'eng', got %q", e.Language)
	}
	if !strings.Contains(e.ImageURL, "54321") {
		t.Errorf("ImageURL should contain cover 54321, got %q", e.ImageURL)
	}
	if e.NumPages == nil || *e.NumPages != 412 {
		t.Errorf("NumPages: want 412, got %v", e.NumPages)
	}
}

func TestGetEditions_HTTP_Ebook(t *testing.T) {
	resp := editionsResponse{
		Entries: []editionEntry{
			{Key: "/books/OL002M", Title: "Dune Kindle", PhysicalFormat: "Kindle Edition"},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL456W/editions.json": jsonStr(resp),
	})

	editions, err := c.GetEditions(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(editions) != 1 || !editions[0].IsEbook {
		t.Errorf("Kindle edition should be marked as ebook: %+v", editions[0])
	}
}

func TestGetEditions_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/works/OL456W/editions.json": "error"},
		map[string]int{"/works/OL456W/editions.json": http.StatusInternalServerError},
	)
	_, err := c.GetEditions(context.Background(), "OL456W")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

// --- GetBookByISBN ---

func TestGetBookByISBN_HTTP_WithWorkRef(t *testing.T) {
	isbnResp := isbnResponse{
		Key:   "/books/OL001M",
		Title: "Dune (Edition)",
		Works: []struct {
			Key string `json:"key"`
		}{{Key: "/works/OL456W"}},
	}
	workResp := workResponse{
		Key:   "/works/OL456W",
		Title: "Dune",
	}
	authorResp := authorResponse{
		Key:  "/authors/OL123A",
		Name: "Frank Herbert",
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/isbn/9780441013593.json": jsonStr(isbnResp),
		"/works/OL456W.json":       jsonStr(workResp),
		"/authors/OL123A.json":     jsonStr(authorResp),
	})

	book, err := c.GetBookByISBN(context.Background(), "9780441013593")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("expected non-nil book")
		return
	}
	// Should resolve the work, not the edition stub
	if book.Title != "Dune" {
		t.Errorf("Title: want 'Dune', got %q", book.Title)
	}
}

func TestGetBookByISBN_HTTP_FallbackNoWork(t *testing.T) {
	// ISBN response with no Works[] → construct from edition data directly.
	isbnResp := isbnResponse{
		Key:    "/books/OL999M",
		Title:  "Standalone Edition",
		Covers: []int{11111},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/isbn/0441013597.json": jsonStr(isbnResp),
	})

	book, err := c.GetBookByISBN(context.Background(), "0441013597")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book.Title != "Standalone Edition" {
		t.Errorf("Title: want 'Standalone Edition', got %q", book.Title)
	}
	if !strings.Contains(book.ImageURL, "11111") {
		t.Errorf("ImageURL should contain cover 11111, got %q", book.ImageURL)
	}
}

// A 404 from OpenLibrary means "this ISBN is not in their catalog" — not an
// upstream failure. GetBookByISBN returns (nil, nil) so the API layer can
// respond with a user-friendly "no book found" message (see issue #284).
func TestGetBookByISBN_HTTP_NotFound(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/isbn/0000000000.json": "not found"},
		map[string]int{"/isbn/0000000000.json": http.StatusNotFound},
	)
	book, err := c.GetBookByISBN(context.Background(), "0000000000")
	if err != nil {
		t.Fatalf("expected nil error for 404, got %v", err)
	}
	if book != nil {
		t.Fatalf("expected nil book for 404, got %+v", book)
	}
}

// --- GetBook ---

func TestGetBook_HTTP_WithAuthor(t *testing.T) {
	workResp := workResponse{
		Key:      "/works/OL20617889W",
		Title:    "The Shining",
		Subjects: []string{"Horror", "Fiction"},
		Authors: []workAuthor{{Author: struct {
			Key string `json:"key"`
		}{Key: "/authors/OL26320A"}}},
		Series: []string{"The Shining #1"},
	}
	authorResp := authorResponse{
		Key:    "/authors/OL26320A",
		Name:   "Stephen King",
		Photos: []int{},
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL20617889W.json": jsonStr(workResp),
		"/authors/OL26320A.json":  jsonStr(authorResp),
	})

	book, err := c.GetBook(context.Background(), "OL20617889W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Title != "The Shining" {
		t.Errorf("Title: want 'The Shining', got %q", book.Title)
	}
	if book.Author == nil || book.Author.Name != "Stephen King" {
		t.Errorf("Author: %+v", book.Author)
	}
	if len(book.SeriesRefs) != 1 {
		t.Errorf("expected 1 series ref, got %d", len(book.SeriesRefs))
	} else {
		if book.SeriesRefs[0].Title != "The Shining" {
			t.Errorf("SeriesRef.Title: want 'The Shining', got %q", book.SeriesRefs[0].Title)
		}
		if !book.SeriesRefs[0].Primary {
			t.Error("first series ref should be primary")
		}
	}
}

func TestGetBook_HTTP_NoAuthor(t *testing.T) {
	workResp := workResponse{
		Key:   "/works/OL999W",
		Title: "No Author Book",
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL999W.json": jsonStr(workResp),
	})

	book, err := c.GetBook(context.Background(), "OL999W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book.Author != nil {
		t.Errorf("expected nil Author when no authors in response, got %+v", book.Author)
	}
}

func TestGetBook_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/works/OL404W.json": "not found"},
		map[string]int{"/works/OL404W.json": http.StatusNotFound},
	)
	_, err := c.GetBook(context.Background(), "OL404W")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

// ctxAwareTransport is a RoundTripper that honors request-context cancellation
// the way a real network transport does: if the context is already done it
// returns its error without "dialing", otherwise it serves a fixed body. Used
// to exercise the context-cancel path deterministically (the path-routing test
// transport ignores the context).
type ctxAwareTransport struct {
	t       *testing.T
	dialled *bool
}

func (tr *ctxAwareTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if tr.dialled != nil {
		*tr.dialled = true
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
	}, nil
}

// TestGetBook_HTTP_ContextCanceled verifies that an already-cancelled context
// makes GetBook return promptly with an error instead of hanging or returning
// a nil error, and that the transport is never reached.
//
// Note: getJSON rebuilds transport errors via errors.New(RedactSecrets(...)),
// which flattens the error chain — so errors.Is(err, context.Canceled) does
// NOT hold here and we assert on the message instead.
func TestGetBook_HTTP_ContextCanceled(t *testing.T) {
	var dialled bool
	c := &Client{http: &http.Client{Transport: &ctxAwareTransport{t: t, dialled: &dialled}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	book, err := c.GetBook(ctx, "OL456W")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected error to mention context cancellation, got %v", err)
	}
	if book != nil {
		t.Errorf("expected nil book on cancellation, got %+v", book)
	}
	if dialled {
		t.Error("transport must not be reached when the context is already cancelled")
	}
}

func TestGetBook_HTTP_CoverImage(t *testing.T) {
	workResp := workResponse{
		Key:    "/works/OL777W",
		Title:  "Covered Book",
		Covers: []int{77777},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OL777W.json": jsonStr(workResp),
	})

	book, err := c.GetBook(context.Background(), "OL777W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if !strings.Contains(book.ImageURL, "77777") {
		t.Errorf("ImageURL should contain cover ID 77777, got %q", book.ImageURL)
	}
}

// --- GetAuthorWorks ---

// searchDocForAuthor mirrors the anonymous struct searchAuthorWorks decodes
// into. Keeping it here lets individual tests construct primary responses
// without re-declaring the shape each time.
type searchDocForAuthor struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Language         []string `json:"language"`
	EditionCount     int      `json:"edition_count"`
	FirstPublishYear int      `json:"first_publish_year"`
	CoverI           *int     `json:"cover_i"`
	Subject          []string `json:"subject"`
}

type searchRespForAuthor struct {
	Docs []searchDocForAuthor `json:"docs"`
}

func TestGetAuthorWorks_HTTP(t *testing.T) {
	coverI := 12345
	// Primary source: /authors/{id}/works.json — includes series membership.
	worksResp := authorWorksResponse{
		Size: 2,
		Entries: []authorWorkEntry{
			{
				Key:    "/works/OL456W",
				Title:  "Dune",
				Series: []string{"Dune Chronicles #1"},
			},
			{
				Key:   "/works/OL789W",
				Title: "Dune Messiah",
			},
		},
	}
	// Enrichment: /search adds language + cover + year not in the works list.
	searchResp := searchRespForAuthor{
		Docs: []searchDocForAuthor{
			{
				Key:              "/works/OL456W",
				Title:            "Dune",
				Language:         []string{"eng"},
				FirstPublishYear: 1965,
				CoverI:           &coverI,
				Subject:          []string{"Sci-Fi"},
			},
			{
				Key:      "/works/OL789W",
				Title:    "Dune Messiah",
				Language: []string{"eng"},
			},
		},
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A/works.json": jsonStr(worksResp),
		"/search.json":               jsonStr(searchResp),
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("expected 2 works, got %d", len(books))
	}
	if books[0].Title != "Dune" {
		t.Errorf("first book title: want 'Dune', got %q", books[0].Title)
	}
	if books[0].Language != "eng" {
		t.Errorf("first book language: want 'eng', got %q", books[0].Language)
	}
	if !strings.Contains(books[0].ImageURL, "12345") {
		t.Errorf("first book ImageURL should contain cover 12345, got %q", books[0].ImageURL)
	}
	if books[0].ReleaseDate == nil || books[0].ReleaseDate.Year() != 1965 {
		t.Errorf("first book ReleaseDate should be 1965, got %v", books[0].ReleaseDate)
	}
	if len(books[0].SeriesRefs) != 1 {
		t.Fatalf("expected 1 series ref for Dune (from primary works endpoint), got %d", len(books[0].SeriesRefs))
	}
	if books[0].SeriesRefs[0].Title != "Dune Chronicles" {
		t.Errorf("series title: want 'Dune Chronicles', got %q", books[0].SeriesRefs[0].Title)
	}
	if books[0].Author == nil || books[0].Author.ForeignID != "OL123A" {
		t.Errorf("author reference not populated: %+v", books[0].Author)
	}
}

func TestGetAuthorWorks_HTTP_FetchesSourcesConcurrently(t *testing.T) {
	searchResp := searchRespForAuthor{Docs: []searchDocForAuthor{{Key: "/works/OL1W", Title: "Primary"}}}
	worksResp := authorWorksResponse{Entries: []authorWorkEntry{{Key: "/works/OL1W", Title: "Primary"}}}

	var mu sync.Mutex
	var once sync.Once
	var timedOut atomic.Bool
	started := map[string]bool{}
	bothStarted := make(chan struct{})
	markStarted := func(path string) {
		mu.Lock()
		defer mu.Unlock()
		started[path] = true
		if started["/search.json"] && started["/authors/OL123A/works.json"] {
			once.Do(func() { close(bothStarted) })
		}
	}
	waitForPeer := func(path, body string) func(*http.Request) string {
		return func(r *http.Request) string {
			markStarted(path)
			select {
			case <-bothStarted:
			case <-time.After(500 * time.Millisecond):
				timedOut.Store(true)
			}
			return body
		}
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json":               waitForPeer("/search.json", jsonStr(searchResp)),
		"/authors/OL123A/works.json": waitForPeer("/authors/OL123A/works.json", jsonStr(worksResp)),
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if timedOut.Load() {
		t.Fatal("expected search and works requests to overlap")
	}
	if len(books) != 1 || books[0].Title != "Primary" {
		t.Fatalf("unexpected books: %+v", books)
	}
}

// Works present only in the /authors/{id}/works endpoint (not in the search
// index) must still be returned — the primary works source covers recent
// releases even when the search index hasn't caught up.
func TestGetAuthorWorks_HTTP_WorksEndpointFillsMissingSearchEntries(t *testing.T) {
	worksResp := authorWorksResponse{
		Entries: []authorWorkEntry{
			{Key: "/works/OLOLDW", Title: "Older Book"},
			{Key: "/works/OLNEWW", Title: "Recently Released"}, // not in search index
		},
	}
	searchResp := searchRespForAuthor{
		Docs: []searchDocForAuthor{
			{Key: "/works/OLOLDW", Title: "Older Book", Language: []string{"eng"}},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A/works.json": jsonStr(worksResp),
		"/search.json":               jsonStr(searchResp),
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("expected 2 books (works primary + search enrichment), got %d", len(books))
	}
	var got []string
	for _, b := range books {
		got = append(got, b.Title)
	}
	if got[0] != "Older Book" || got[1] != "Recently Released" {
		t.Errorf("expected [Older Book, Recently Released] in that order, got %v", got)
	}
}

// Authors with more than one page of works must have their full catalogue
// fetched, not just the first 100. Regression guard for the hard 100-book cap
// reported on prolific authors (the works endpoint was fetched once, never
// paged). The mock pages by ?offset= and advertises the total via Size.
func TestGetAuthorWorks_HTTP_PaginatesPastFirstPage(t *testing.T) {
	const total = 250
	worksHandler := func(r *http.Request) string {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var entries []authorWorkEntry
		for i := offset; i < offset+authorWorksPageSize && i < total; i++ {
			entries = append(entries, authorWorkEntry{
				Key:   "/works/OL" + strconv.Itoa(i) + "W",
				Title: "Book " + strconv.Itoa(i),
			})
		}
		return jsonStr(authorWorksResponse{Size: total, Entries: entries})
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A/works.json": worksHandler,
		"/search.json":               jsonStr(searchRespForAuthor{}),
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != total {
		t.Fatalf("expected %d books across all pages, got %d (cap regression?)", total, len(books))
	}
}

// Works present only in the search index (when /authors/{id}/works is empty)
// are still returned as a fallback — the search enrichment source stands on
// its own when the works endpoint returns nothing.
func TestGetAuthorWorks_HTTP_SearchFallbackWhenWorksEmpty(t *testing.T) {
	searchResp := searchRespForAuthor{
		Docs: []searchDocForAuthor{
			{Key: "/works/OL456W", Title: "Dune", Language: []string{"eng"}},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A/works.json": jsonStr(authorWorksResponse{}),
		"/search.json":               jsonStr(searchResp),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Dune" {
		t.Fatalf("expected 1 book from search fallback, got %+v", books)
	}
}

// TestGetAuthorWorks_HTTP_ObjectSeries covers the schema variance seen with
// Pierce Brown where OpenLibrary returns series as [{key,title}] objects
// instead of plain strings. The parser should not error and should extract the
// title when present. The series data comes from the works endpoint (primary);
// the search endpoint enriches with language.
func TestGetAuthorWorks_HTTP_ObjectSeries(t *testing.T) {
	worksBody := `{
		"size": 1,
		"entries": [{
			"key": "/works/OL12345W",
			"title": "Red Rising",
			"series": [{"key": "/works/OL9999W", "title": "Red Rising #1"}]
		}]
	}`
	searchBody := `{"docs":[{"key":"/works/OL12345W","title":"Red Rising","language":["eng"]}]}`

	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL999A/works.json": worksBody,
		"/search.json":               searchBody,
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthorWorks with object series: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if books[0].Title != "Red Rising" {
		t.Errorf("title: want 'Red Rising', got %q", books[0].Title)
	}
	if len(books[0].SeriesRefs) != 1 {
		t.Errorf("expected 1 series ref, got %d", len(books[0].SeriesRefs))
	}
	if books[0].SeriesRefs[0].Title != "Red Rising" {
		t.Errorf("series title: want 'Red Rising', got %q", books[0].SeriesRefs[0].Title)
	}
	if books[0].SeriesRefs[0].Position != "1" {
		t.Errorf("series position: want '1', got %q", books[0].SeriesRefs[0].Position)
	}
}

// Both endpoints failing is the only case where GetAuthorWorks reports an
// error. Single-endpoint failures are logged and the healthy side is used.
func TestGetAuthorWorks_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{
			"/authors/OL404A/works.json": "error",
			"/search.json":               "error",
		},
		map[string]int{
			"/authors/OL404A/works.json": http.StatusInternalServerError,
			"/search.json":               http.StatusInternalServerError,
		},
	)
	_, err := c.GetAuthorWorks(context.Background(), "OL404A")
	if err == nil {
		t.Fatal("expected error when both endpoints fail")
	}
}

// A failure in the search enrichment endpoint must not abort ingestion —
// the /authors/{id}/works endpoint alone is enough to populate the catalogue,
// including series data. This matches the pre-#408 backfill-failure behaviour
// but with roles reversed: works is now primary, search is enrichment.
func TestGetAuthorWorks_HTTP_SearchEnrichmentFailure(t *testing.T) {
	worksResp := authorWorksResponse{
		Entries: []authorWorkEntry{
			{Key: "/works/OL1W", Title: "Only In Works", Series: []string{"MySeries #1"}},
		},
	}
	c := newClientWithStatus(t,
		map[string]interface{}{
			"/authors/OL1A/works.json": jsonStr(worksResp),
			"/search.json":             "oops",
		},
		map[string]int{
			"/search.json": http.StatusInternalServerError,
		},
	)
	books, err := c.GetAuthorWorks(context.Background(), "OL1A")
	if err != nil {
		t.Fatalf("expected no error when only search enrichment fails: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Only In Works" {
		t.Errorf("expected works endpoint to still return, got %+v", books)
	}
	if len(books[0].SeriesRefs) != 1 {
		t.Errorf("expected series ref from works endpoint, got %d", len(books[0].SeriesRefs))
	}
}

func TestGetAuthorWorks_HTTP_LangPreferEng(t *testing.T) {
	// Search enrichment returns a work with multiple languages — "eng" wins
	// regardless of ordering.
	worksResp := authorWorksResponse{
		Entries: []authorWorkEntry{
			{Key: "/works/OL100W", Title: "Multi-lang Book"},
		},
	}
	searchResp := searchRespForAuthor{
		Docs: []searchDocForAuthor{
			{Key: "/works/OL100W", Title: "Multi-lang Book", Language: []string{"fre", "ger", "eng"}},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL999A/works.json": jsonStr(worksResp),
		"/search.json":               jsonStr(searchResp),
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}
	if books[0].Language != "eng" {
		t.Errorf("Language: want 'eng', got %q", books[0].Language)
	}
}

// Noise filter: study guides, summaries, and adaptations must be dropped
// before they reach the ingestion pipeline. Subject-based and title-based
// markers are both exercised here. Noise comes from the works endpoint
// (primary source) and must be filtered before merging.
func TestGetAuthorWorks_HTTP_NoiseFilter(t *testing.T) {
	worksResp := authorWorksResponse{
		Entries: []authorWorkEntry{
			{Key: "/works/OLREAL1W", Title: "The Dutch House"},
			{Key: "/works/OLJUNK1W", Title: "Summary of The Dutch House"},
			{Key: "/works/OLJUNK2W", Title: "A Reader's Guide to Commonwealth"},
			{Key: "/works/OLJUNK3W", Title: "Film Companion", Subjects: []string{"Motion picture adaptations"}},
			{Key: "/works/OLJUNK4W", Title: "The Dutch House (Audio CD)"},
			{Key: "/works/OLJUNK5W", Title: "Commonwealth", Subjects: []string{"Study guides"}},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL5A/works.json": jsonStr(worksResp),
		"/search.json":             jsonStr(searchRespForAuthor{}),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL5A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 book to survive noise filter, got %d: %+v", len(books), books)
	}
	if books[0].Title != "The Dutch House" {
		t.Errorf("surviving book: want 'The Dutch House', got %q", books[0].Title)
	}
}

// Noise filter also drops entries that come in via the search enrichment
// source (not just the primary works results) — important because the search
// index has its own share of tie-in companions.
func TestGetAuthorWorks_HTTP_NoiseFilterSearchEnrichment(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL6A/works.json": jsonStr(authorWorksResponse{
			Entries: []authorWorkEntry{
				{Key: "/works/OL1W", Title: "Real Book"},
			},
		}),
		"/search.json": jsonStr(searchRespForAuthor{
			Docs: []searchDocForAuthor{
				{Key: "/works/OL1W", Title: "Real Book", Language: []string{"eng"}},
				// These noise entries only appear in search (not in works) and must be dropped.
				{Key: "/works/OL2W", Title: "CliffsNotes on Real Book"},
				{Key: "/works/OL3W", Title: "Some Film Tie-in", Subject: []string{"Film adaptations"}},
			},
		}),
	})
	books, err := c.GetAuthorWorks(context.Background(), "OL6A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Real Book" {
		t.Fatalf("expected only 'Real Book' to survive, got %+v", books)
	}
}

// --- FillMissingWorkLanguages (edition sampling, #891) ---

func langEntries(codes ...string) []editionEntry {
	entries := make([]editionEntry, 0, len(codes))
	for _, code := range codes {
		e := editionEntry{Key: "/books/OLxM"}
		if code != "" {
			e.Languages = []struct {
				Key string `json:"key"`
			}{{Key: "/languages/" + code}}
		}
		entries = append(entries, e)
	}
	return entries
}

// A work that has no work-level language but whose editions are foreign gets
// the dominant edition language, so the allowed_languages filter can catch it.
func TestFillMissingWorkLanguages_DerivesFromEditions(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OLSPAW/editions.json": jsonStr(editionsResponse{
			Entries: langEntries("spa", "spa", "eng"),
		}),
	})
	c.workLangCache = map[string]string{}

	books := []models.Book{{ForeignID: "OLSPAW", Title: "Spanish-only Work"}}
	filled := c.FillMissingWorkLanguages(context.Background(), books)
	if filled != 1 {
		t.Fatalf("expected 1 book filled, got %d", filled)
	}
	if books[0].Language != "spa" {
		t.Errorf("Language: want 'spa' (majority edition language), got %q", books[0].Language)
	}
}

// A work with no language anywhere (no work-level, no edition languages) stays
// empty so the caller's unknown-language behavior still applies (pass).
func TestFillMissingWorkLanguages_NoLanguageStaysUnknown(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OLNOLANGW/editions.json": jsonStr(editionsResponse{
			Entries: langEntries("", ""),
		}),
	})
	c.workLangCache = map[string]string{}

	books := []models.Book{{ForeignID: "OLNOLANGW", Title: "No Language Work"}}
	filled := c.FillMissingWorkLanguages(context.Background(), books)
	if filled != 0 {
		t.Fatalf("expected 0 books filled, got %d", filled)
	}
	if books[0].Language != "" {
		t.Errorf("Language: want '' (unknown), got %q", books[0].Language)
	}
}

// Books that already have a language are not sampled, and the editions endpoint
// is only hit once per work (cap respected): the limit query param is bounded
// and a cached miss is not re-fetched.
func TestFillMissingWorkLanguages_BoundedAndCached(t *testing.T) {
	var calls int32
	var gotURL string
	c := newClientWithPaths(t, map[string]interface{}{
		"/works/OLSAMPLEW/editions.json": func(r *http.Request) string {
			atomic.AddInt32(&calls, 1)
			gotURL = r.URL.String()
			return jsonStr(editionsResponse{Entries: langEntries("fre")})
		},
		"/works/OLHASLANGW/editions.json": func(r *http.Request) string {
			t.Error("should not sample a work that already has a language")
			return "{}"
		},
	})
	c.workLangCache = map[string]string{}

	books := []models.Book{
		{ForeignID: "OLHASLANGW", Title: "Already English", Language: "eng"},
		{ForeignID: "OLSAMPLEW", Title: "Needs Sampling"},
		{ForeignID: "", Title: "No Foreign ID"},
	}

	// First pass derives the language; second pass must reuse the cache.
	if filled := c.FillMissingWorkLanguages(context.Background(), books); filled != 1 {
		t.Fatalf("first pass: expected 1 filled, got %d", filled)
	}
	books[1].Language = "" // reset to force a re-derive attempt
	c.FillMissingWorkLanguages(context.Background(), books)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("editions endpoint should be hit once (cached after), hit %d times", got)
	}
	if !strings.Contains(gotURL, "limit=5") {
		t.Errorf("sampling should bound editions with limit=5, got URL %q", gotURL)
	}
	if books[1].Language != "fre" {
		t.Errorf("Language: want 'fre', got %q", books[1].Language)
	}
}

// A failure fetching editions leaves the work unknown (caller's fallback wins)
// and the miss is cached so a flaky/expensive endpoint isn't retried this run.
func TestFillMissingWorkLanguages_FetchErrorCachesMiss(t *testing.T) {
	var calls int32
	c := newClientWithStatus(t,
		map[string]interface{}{
			"/works/OLERRW/editions.json": func(r *http.Request) string {
				atomic.AddInt32(&calls, 1)
				return "boom"
			},
		},
		map[string]int{"/works/OLERRW/editions.json": http.StatusInternalServerError},
	)
	c.workLangCache = map[string]string{}

	books := []models.Book{{ForeignID: "OLERRW", Title: "Flaky Work"}}
	c.FillMissingWorkLanguages(context.Background(), books)
	books[0].Language = ""
	c.FillMissingWorkLanguages(context.Background(), books)

	if books[0].Language != "" {
		t.Errorf("Language should stay empty on fetch error, got %q", books[0].Language)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("a failed sample should be cached, not retried; hit %d times", got)
	}
}

func TestMajorityEditionLanguage_PrefersEngOnTie(t *testing.T) {
	got := majorityEditionLanguage(langEntries("eng", "fre"))
	if got != "eng" {
		t.Errorf("on a tie 'eng' should win, got %q", got)
	}
}

// --- GetSubjectBooks ---

func TestGetSubjectBooks_HTTP(t *testing.T) {
	coverID := 99999
	resp := subjectBooksResponse{
		Name:      "Fantasy",
		WorkCount: 2,
		Works: []subjectWork{
			{
				Key:              "/works/OL1111W",
				Title:            "Popular Fantasy",
				CoverID:          &coverID,
				FirstPublishYear: 2015,
				Subject:          []string{"Fantasy", "Adventure"},
				Authors: []struct {
					Key  string `json:"key"`
					Name string `json:"name"`
				}{
					{Key: "/authors/OL10A", Name: "Famous Author"},
				},
			},
			{
				Key:   "/works/OL2222W",
				Title: "No Cover Work",
			},
		},
	}

	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/fantasy.json": jsonStr(resp),
	})

	candidates, err := c.GetSubjectBooks(context.Background(), "fantasy", 20)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	first := candidates[0]
	if first.ForeignID != "OL1111W" {
		t.Errorf("ForeignID: want 'OL1111W', got %q", first.ForeignID)
	}
	if first.Title != "Popular Fantasy" {
		t.Errorf("Title: want 'Popular Fantasy', got %q", first.Title)
	}
	if first.AuthorName != "Famous Author" {
		t.Errorf("AuthorName: want 'Famous Author', got %q", first.AuthorName)
	}
	if first.ReleaseDate == nil || first.ReleaseDate.Year() != 2015 {
		t.Errorf("ReleaseDate: expected 2015, got %v", first.ReleaseDate)
	}
	if !strings.Contains(first.ImageURL, "99999") {
		t.Errorf("ImageURL should contain cover ID 99999, got %q", first.ImageURL)
	}
	if first.MediaType != "ebook" {
		t.Errorf("MediaType: want 'ebook', got %q", first.MediaType)
	}
	if len(first.Genres) != 2 {
		t.Errorf("Genres: want 2, got %d", len(first.Genres))
	}

	// Second candidate: no cover, no authors.
	second := candidates[1]
	if second.ImageURL != "" {
		t.Errorf("second ImageURL should be empty, got %q", second.ImageURL)
	}
	if second.AuthorName != "" {
		t.Errorf("second AuthorName should be empty, got %q", second.AuthorName)
	}
}

func TestGetSubjectBooks_HTTP_DefaultLimit(t *testing.T) {
	// Limit <= 0 defaults to 20. Assert the URL passed to the server contains limit=20.
	var gotURL string
	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/scifi.json": func(r *http.Request) string {
			gotURL = r.URL.String()
			return jsonStr(subjectBooksResponse{})
		},
	})

	_, err := c.GetSubjectBooks(context.Background(), "scifi", 0)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if !strings.Contains(gotURL, "limit=20") {
		t.Errorf("URL should default limit to 20, got %q", gotURL)
	}
}

func TestGetSubjectBooks_HTTP_Error(t *testing.T) {
	c := newClientWithStatus(t,
		map[string]interface{}{"/subjects/horror.json": "oops"},
		map[string]int{"/subjects/horror.json": http.StatusInternalServerError},
	)
	_, err := c.GetSubjectBooks(context.Background(), "horror", 5)
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestGetSubjectBooks_HTTP_Empty(t *testing.T) {
	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/obscure.json": jsonStr(subjectBooksResponse{Name: "Obscure"}),
	})
	candidates, err := c.GetSubjectBooks(context.Background(), "obscure", 5)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestGetSubjectBooks_HTTP_NegativeCover(t *testing.T) {
	// OpenLibrary sometimes returns cover_id=-1 meaning "no cover". Must not build a URL.
	negCover := -1
	resp := subjectBooksResponse{
		Works: []subjectWork{
			{Key: "/works/OL9W", Title: "No Cover", CoverID: &negCover},
		},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/subjects/romance.json": jsonStr(resp),
	})

	candidates, err := c.GetSubjectBooks(context.Background(), "romance", 5)
	if err != nil {
		t.Fatalf("GetSubjectBooks: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].ImageURL != "" {
		t.Errorf("negative cover ID should produce empty ImageURL, got %q", candidates[0].ImageURL)
	}
}

// --- Helper functions ---

func TestName_OL(t *testing.T) {
	c := New()
	if c.Name() != "openlibrary" {
		t.Errorf("Name: want 'openlibrary', got %q", c.Name())
	}
}

func TestTruncateSlice(t *testing.T) {
	s := []string{"a", "b", "c", "d", "e"}
	got := truncateSlice(s, 3)
	if len(got) != 3 {
		t.Errorf("truncateSlice(5, 3) = %d, want 3", len(got))
	}
	// No truncation when within limit
	got2 := truncateSlice(s, 10)
	if len(got2) != 5 {
		t.Errorf("truncateSlice(5, 10) = %d, want 5", len(got2))
	}
	// nil input returns empty slice
	got3 := truncateSlice(nil, 5)
	if got3 == nil {
		t.Error("truncateSlice(nil) should return [] not nil")
	}
}

func TestFirst(t *testing.T) {
	if first([]string{"a", "b"}) != "a" {
		t.Error("first([a,b]) != a")
	}
	if first(nil) != "" {
		t.Error("first(nil) != ''")
	}
}

func TestNilIfZero(t *testing.T) {
	if nilIfZero(0) != nil {
		t.Error("nilIfZero(0) should be nil")
	}
	p := nilIfZero(5)
	if p == nil || *p != 5 {
		t.Errorf("nilIfZero(5) = %v", p)
	}
}

// TestGetAuthorWorks_HTTP_CreditedAuthors verifies that the works endpoint's
// authors array and the search index's author_key list both surface as
// CreditedAuthorForeignIDs — the signal the author sync uses to tell a
// co-authored work apart from a row mis-parented under another author (#1405).
func TestGetAuthorWorks_HTTP_CreditedAuthors(t *testing.T) {
	worksBody := `{
		"size": 1,
		"entries": [{
			"key": "/works/OL700W",
			"title": "Co-Written Book",
			"authors": [
				{"author": {"key": "/authors/OL123A"}},
				{"author": {"key": "/authors/OL999A"}}
			]
		}]
	}`
	// One doc enriches the primary work; the second exists only in the
	// search index and must carry its author_key list through the
	// enrichment-only append.
	searchBody := `{
		"docs": [
			{"key": "/works/OL700W", "title": "Co-Written Book", "author_key": ["OL123A", "OL999A"]},
			{"key": "/works/OL701W", "title": "Search Only Book", "author_key": ["OL123A"]}
		]
	}`
	c := newClientWithPaths(t, map[string]interface{}{
		"/authors/OL123A/works.json": worksBody,
		"/search.json":               searchBody,
	})

	books, err := c.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	byID := map[string][]string{}
	for _, b := range books {
		byID[b.ForeignID] = b.CreditedAuthorForeignIDs
	}
	if got := byID["OL700W"]; !slices.Equal(got, []string{"OL123A", "OL999A"}) {
		t.Errorf("primary work credited authors: got %v", got)
	}
	if got := byID["OL701W"]; !slices.Equal(got, []string{"OL123A"}) {
		t.Errorf("enrichment-only work credited authors: got %v", got)
	}
}
