package ranobedb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
)

// TestSearchBooksTieWithMatchingNumbers: two results score the same and both
// carry the book's own volume number, so the volume guard passes and only the
// tie rule stands between the search and a coin flip. The book record is never
// asked for.
func TestSearchBooksTieWithMatchingNumbers(t *testing.T) {
	api := &fakeAPI{t: t, searches: map[string]string{
		"Spice and Wolf, Vol. 3": `{"books":[` +
			`{"id":3188,"title":"Spice and Wolf, Vol. 3","sim_score":1},` +
			`{"id":9999,"title":"Spice and Wolf, Vol. 3","sim_score":1}]}`,
	}, books: map[string]string{"/book/3188": fixture(t, "book_3188.json")}}
	books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
	if err != nil || books != nil {
		t.Fatalf("got %v, %v; want no match", books, err)
	}
	if len(api.paths) != 1 {
		t.Errorf("requests = %v, want the search only", api.paths)
	}
}

func TestNewClientIsAnEnricherOnly(t *testing.T) {
	c := New()
	if c.Name() != "ranobedb" || c.base != apiBase || c.http.Timeout != requestTimeout {
		t.Errorf("client = %+v", c)
	}
	var _ metadata.Provider = c
	ctx := context.Background()
	if a, err := c.SearchAuthors(ctx, "x"); a != nil || err != nil {
		t.Errorf("SearchAuthors = %v, %v", a, err)
	}
	if a, err := c.GetAuthor(ctx, "x"); a != nil || err != nil {
		t.Errorf("GetAuthor = %v, %v", a, err)
	}
	if b, err := c.GetBook(ctx, "x"); b != nil || err != nil {
		t.Errorf("GetBook = %v, %v", b, err)
	}
	if e, err := c.GetEditions(ctx, "x"); e != nil || err != nil {
		t.Errorf("GetEditions = %v, %v", e, err)
	}
	if b, err := c.GetBookByISBN(ctx, "x"); b != nil || err != nil {
		t.Errorf("GetBookByISBN = %v, %v", b, err)
	}
}

func TestSearchBooksNilClientAndBlankQuery(t *testing.T) {
	var nilClient *Client
	if _, err := nilClient.SearchBooks(context.Background(), "x"); !errors.Is(err, metadata.ErrProviderNotConfigured) {
		t.Errorf("nil client err = %v", err)
	}
	api := spiceAPI(t)
	books, err := testClient(t, api).SearchBooks(context.Background(), "   ")
	if books != nil || err != nil || len(api.paths) != 0 {
		t.Errorf("blank query: %v, %v, requests %v", books, err, api.paths)
	}
}

// TestSearchBooksFailsSoft: every failure comes back as an error and no books,
// so the aggregator skips the enricher and the book keeps its own title.
func TestSearchBooksFailsSoft(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		api := spiceAPI(t)
		api.status = http.StatusServiceUnavailable
		books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
		if books != nil || err == nil {
			t.Errorf("got %v, %v", books, err)
		}
	})
	t.Run("not json", func(t *testing.T) {
		api := spiceAPI(t)
		api.searches["Spice and Wolf, Vol. 3"] = "<html>nope</html>"
		books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
		if books != nil || err == nil {
			t.Errorf("got %v, %v", books, err)
		}
	})
	t.Run("book record fails", func(t *testing.T) {
		api := spiceAPI(t)
		delete(api.books, "/book/3188")
		books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
		if books != nil || err == nil {
			t.Errorf("got %v, %v", books, err)
		}
	})
	t.Run("network failure", func(t *testing.T) {
		restore := providerhttp.SetBaseDelay(time.Millisecond)
		defer restore()
		srv := httptest.NewServer(http.NotFoundHandler())
		base := srv.URL
		srv.Close()
		c := &Client{http: &http.Client{Timeout: time.Second}, gate: providerhttp.NewGate(), base: base}
		_, err := c.SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
		if !errors.Is(err, providererr.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
}

// TestResponseSizeIsLimited: a body past the cap is cut off and fails to
// decode, rather than being read whole.
func TestResponseSizeIsLimited(t *testing.T) {
	api := spiceAPI(t)
	api.searches["Spice and Wolf, Vol. 3"] = `{"books":[],"pad":"` + strings.Repeat("a", maxResponseBytes) + `"}`
	books, err := testClient(t, api).SearchBooks(context.Background(), "Spice and Wolf, Vol. 3")
	if books != nil || err == nil {
		t.Errorf("got %v, %v; an oversized body was accepted", books, err)
	}
}

func TestClassify(t *testing.T) {
	if err := classify(context.Canceled); !errors.Is(err, context.Canceled) || errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("canceled = %v", err)
	}
	if err := classify(errPaced); !errors.Is(err, providererr.ErrRateLimited) {
		t.Errorf("paced = %v", err)
	}
	if err := classify(errors.New("boom")); !errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("other = %v", err)
	}
}

type fakeTransport struct {
	resp *http.Response
	err  error
}

func (f fakeTransport) RoundTrip(*http.Request) (*http.Response, error) { return f.resp, f.err }

// TestPacedTransportHoldsTheSlotUntilTheBodyCloses: the next request cannot
// start while a response body is still open, and a transport error frees it.
func TestPacedTransportHoldsTheSlotUntilTheBodyCloses(t *testing.T) {
	p := &pacer{interval: time.Millisecond, slot: make(chan struct{}, 1)}
	req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)

	body := io.NopCloser(strings.NewReader("{}"))
	tr := &pacedTransport{base: fakeTransport{resp: &http.Response{StatusCode: 200, Body: body}}, pacer: p}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.acquire(ctx); err == nil {
		t.Fatal("slot was free while a response body was open")
	}
	_ = resp.Body.Close()
	_ = resp.Body.Close() // idempotent
	rel, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("slot not freed by Close: %v", err)
	}
	rel()

	failing := &pacedTransport{base: fakeTransport{err: errors.New("dial")}, pacer: p}
	if r, err := failing.RoundTrip(req); err == nil {
		_ = r.Body.Close()
		t.Fatal("want the transport error")
	}
	rel, err = p.acquire(context.Background())
	if err != nil {
		t.Fatalf("slot not freed by a transport error: %v", err)
	}
	rel()

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if r, err := tr.RoundTrip(req.WithContext(ctx)); err == nil {
		_ = r.Body.Close()
		t.Error("a canceled request was sent")
	}
}

func TestSameHostRedirect(t *testing.T) {
	first := httptest.NewRequest(http.MethodGet, "https://ranobedb.org/api/v0/books", nil)
	same := httptest.NewRequest(http.MethodGet, "https://ranobedb.org/api/v0/books?x=1", nil)
	other := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/admin", nil)
	if err := sameHostRedirect(same, []*http.Request{first}); err != nil {
		t.Errorf("same host refused: %v", err)
	}
	if err := sameHostRedirect(other, []*http.Request{first}); err == nil {
		t.Error("a redirect to another host was followed")
	}
	if err := sameHostRedirect(same, []*http.Request{first, first, first}); err == nil {
		t.Error("a redirect chain was followed without end")
	}
	if New().http.CheckRedirect == nil {
		t.Error("New does not set CheckRedirect")
	}
}
