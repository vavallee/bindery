package nzbfetch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/downloader/nzbfetch"
)

const testNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="x"/></nzb>`

func allowAll(string) error { return nil }

// testClient is NewHTTPClient's redirect policy without the dial guard, which
// would refuse the loopback test servers.
func testClient(validate func(string) error) *http.Client {
	c := nzbfetch.NewHTTPClient(validate)
	c.Transport = nil
	return c
}

func TestFetch_ReturnsNZBAndSendsProjectUserAgent(t *testing.T) {
	var ua atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua.Store(r.Header.Get("User-Agent"))
		fmt.Fprint(w, testNZB)
	}))
	defer srv.Close()

	body, err := nzbfetch.Fetch(context.Background(), testClient(allowAll), allowAll, srv.URL+"/getnzb/1")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != testNZB {
		t.Fatalf("body = %q", body)
	}
	if got, _ := ua.Load().(string); got == "" || strings.HasPrefix(got, "Go-http-client") {
		t.Errorf("User-Agent = %q, want the project agent", got)
	}
}

func TestFetch_RefusedURLIsNeverRequested(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	refuse := func(string) error { return errors.New("url not allowed") }
	_, err := nzbfetch.Fetch(context.Background(), testClient(refuse), refuse, srv.URL+"/getnzb/1")
	if err == nil || !strings.Contains(err.Error(), "fetch nzb: url not allowed") {
		t.Fatalf("err = %v, want the validation error", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("refused URL was requested %d times", hits.Load())
	}
}

// A nil validate is not "no check": it applies the download-fetch policy,
// which refuses loopback unless the operator opted in.
func TestFetch_NilValidateAppliesDownloadFetchPolicy(t *testing.T) {
	t.Setenv("BINDERY_DOWNLOAD_ALLOW_LOOPBACK", "")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	_, err := nzbfetch.Fetch(context.Background(), testClient(nil), nil, srv.URL+"/getnzb/1")
	if err == nil || !strings.Contains(err.Error(), "url not allowed") {
		t.Fatalf("err = %v, want loopback refused", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("refused URL was requested %d times", hits.Load())
	}
}

func TestFetch_NewznabRefusalIsDiagnosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><error code="429" description="Request limit reached"/>`)
	}))
	defer srv.Close()

	_, err := nzbfetch.Fetch(context.Background(), testClient(allowAll), allowAll, srv.URL+"/getnzb/1")
	if err == nil || !strings.Contains(err.Error(), "newznab error 429: Request limit reached") {
		t.Fatalf("err = %v, want the newznab diagnosis", err)
	}
}

func TestFetch_SuccessStatusWithoutNZBIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>Please log in</body></html>")
	}))
	defer srv.Close()

	body, err := nzbfetch.Fetch(context.Background(), testClient(allowAll), allowAll, srv.URL+"/getnzb/1")
	if err == nil {
		t.Fatalf("an HTML page was accepted as an NZB: %q", body)
	}
}

func TestFetch_TransportErrorIsRedacted(t *testing.T) {
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	_, err := nzbfetch.Fetch(context.Background(), failing, allowAll, "http://indexer.example/getnzb/1?apikey=SECRETKEY")
	if err == nil || !strings.Contains(err.Error(), "fetch nzb from indexer") {
		t.Fatalf("err = %v, want a transport error", err)
	}
	if strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("error leaks the apikey: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
