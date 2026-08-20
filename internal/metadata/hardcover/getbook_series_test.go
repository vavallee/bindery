package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// recordingTransport redirects the client's hard-coded GraphQL endpoint at a
// test server and keeps the request bodies, so a test can assert what was
// actually asked for rather than only what came back.
type recordingTransport struct {
	base    *url.URL
	inner   http.RoundTripper
	queries []string
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			var payload struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(body, &payload)
			t.queries = append(t.queries, payload.Query)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		r.ContentLength = int64(len(body))
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	return t.inner.RoundTrip(clone)
}

// newSeriesResponseClient returns a client whose every GraphQL call answers
// with one book carrying a book_series relation, plus the transport recording
// what was asked.
func newSeriesResponseClient(t *testing.T, response string) (*Client, *recordingTransport) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	tr := &recordingTransport{base: base, inner: srv.Client().Transport}
	return (&Client{http: &http.Client{Transport: tr}}).WithToken("hc-token"), tr
}

const bookWithSeriesGQL = `{"data":{"books":[{
	"id": 42,
	"title": "Legendary Rule: Book Two",
	"slug": "legendary-rule",
	"book_series": [{"position": 2, "series": {"id": 7, "name": "Legendary Rule"}}]
}]}}`

// TestGetBook_RequestsAndReturnsSeries is #2116. GetBook's two queries never
// selected book_series, so every work it fetched arrived with SeriesRefs nil.
// AddBook's directInsertSeriesConflict guard takes those refs as its input, so
// it could never fire: adding volume 2 of a series whose volume 1 shares the
// main title was deduped onto volume 1 and silently dropped, with no book
// created and no error shown.
//
// Both halves are asserted. toBook's fallback to bookSeriesRefs already worked,
// so a test that only checked the returned book would have needed the field to
// be requested anyway; asserting the query text as well names the actual defect
// if someone removes the selection later.
func TestGetBook_RequestsAndReturnsSeries(t *testing.T) {
	t.Parallel()
	c, tr := newSeriesResponseClient(t, bookWithSeriesGQL)

	book, err := c.GetBook(context.Background(), "hc:legendary-rule")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if book == nil {
		t.Fatal("GetBook returned nil book")
	}

	if len(tr.queries) == 0 {
		t.Fatal("no GraphQL query recorded")
	}
	if !strings.Contains(tr.queries[0], "book_series") {
		t.Errorf("GetBook query does not select book_series:\n%s", tr.queries[0])
	}

	if len(book.SeriesRefs) != 1 {
		t.Fatalf("SeriesRefs = %d, want 1 — the series conflict guard has nothing to compare without it", len(book.SeriesRefs))
	}
	ref := book.SeriesRefs[0]
	if ref.Title != "Legendary Rule" {
		t.Errorf("series Title = %q, want %q", ref.Title, "Legendary Rule")
	}
	if ref.Position != "2" {
		t.Errorf("series Position = %q, want %q: the guard needs a sequence on both sides to tell two volumes apart", ref.Position, "2")
	}
}

// TestGetBookByISBN_RequestsSeries covers the same omission on the ISBN lookup,
// which the report named alongside GetBook.
func TestGetBookByISBN_RequestsSeries(t *testing.T) {
	t.Parallel()
	const resp = `{"data":{"editions":[{"language":{"language":"English"},"book":{
		"id": 42,
		"title": "Legendary Rule: Book Two",
		"slug": "legendary-rule",
		"book_series": [{"position": 2, "series": {"id": 7, "name": "Legendary Rule"}}]
	}}]}}`
	c, tr := newSeriesResponseClient(t, resp)

	book, err := c.GetBookByISBN(context.Background(), "9781234567890")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if book == nil {
		t.Fatal("GetBookByISBN returned nil book")
	}
	if len(tr.queries) == 0 || !strings.Contains(tr.queries[0], "book_series") {
		t.Errorf("GetBookByISBN query does not select book_series:\n%v", tr.queries)
	}
	if len(book.SeriesRefs) != 1 {
		t.Fatalf("SeriesRefs = %d, want 1", len(book.SeriesRefs))
	}
}

// TestGetAuthorWorksByName_RequestsSeries covers the same omission on the
// author-catalogue supplement. Works fetched by author name arrived with
// SeriesRefs nil, so a Hardcover-supplemented catalogue produced books with no
// series membership at all, and an author on "series" monitor mode never saw
// its pinned-series short circuit fire for them.
//
// The query-text assertion carries weight here beyond regression cover: the
// comment above this query records that requesting a field the `books` type
// lacks makes Hardcover reject the WHOLE query, taking the entire supplement
// down with it. book_series is valid on `books`, which lists.go selects through
// list_books { book { ... } }, and this pins that it is actually asked for.
func TestGetAuthorWorksByName_RequestsSeries(t *testing.T) {
	t.Parallel()
	c, tr := newSeriesResponseClient(t, bookWithSeriesGQL)

	books, err := c.GetAuthorWorksByName(context.Background(), "Bruce Sentar")
	if err != nil {
		t.Fatalf("GetAuthorWorksByName: %v", err)
	}
	if len(tr.queries) == 0 || !strings.Contains(tr.queries[0], "book_series") {
		t.Fatalf("author-works query does not select book_series:\n%v", tr.queries)
	}
	if len(books) == 0 {
		t.Fatal("no books returned")
	}
	if len(books[0].SeriesRefs) != 1 {
		t.Fatalf("SeriesRefs = %d, want 1", len(books[0].SeriesRefs))
	}
	if books[0].SeriesRefs[0].Title != "Legendary Rule" {
		t.Errorf("series Title = %q, want %q", books[0].SeriesRefs[0].Title, "Legendary Rule")
	}
}
