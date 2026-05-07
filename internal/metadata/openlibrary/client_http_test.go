package openlibrary

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
			},
		},
	}
	resp.Docs[0].Editions.Docs = []searchEditionDoc{
		{Key: "/books/OL111M", Title: "Dune - Der Wüstenplanet.", Language: []string{"ger"}},
	}
	c := newClientWithPaths(t, map[string]interface{}{
		"/search.json": func(r *http.Request) string {
			fields := r.URL.Query().Get("fields")
			for _, want := range []string{"editions", "editions.key", "editions.title", "editions.language"} {
				if !strings.Contains(fields, want) {
					t.Fatalf("search fields = %q, want %q", fields, want)
				}
			}
			return jsonStr(resp)
		},
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
	if len(b.Editions) != 1 {
		t.Fatalf("expected 1 matching edition, got %d", len(b.Editions))
	}
	if b.Editions[0].ForeignID != "OL111M" || b.Editions[0].Title != "Dune - Der Wüstenplanet." || b.Editions[0].Language != "ger" {
		t.Errorf("unexpected matching edition: %+v", b.Editions[0])
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
