package sabnzbd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const nzbfinder203Body = `<?xml version="1.0" encoding="UTF-8"?>
<error code="203" description="This application is not allowed to download NZBs from NZBFinder."/>`

// TestAddURL_ProwlarrRedirectRejection reproduces #1404 end to end: the grab
// URL points at a Prowlarr-style proxy which 302-redirects to the indexer
// (Prowlarr's per-indexer Redirect setting), and the indexer rejects Bindery's
// identity with newznab error 203. The resulting error must surface the
// structured newznab error plus the redirect hop and the whitelist guidance
// (#1424), instead of the raw XML soup the log used to carry.
//
// The proxy is addressed as "localhost" and redirects to "127.0.0.1" so the
// two ends of the hop have different hostnames, like prowlarr → nzbfinder.ws.
func TestAddURL_ProwlarrRedirectRejection(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(nzbfinder203Body))
	}))
	defer indexerSrv.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, indexerSrv.URL+"/getnzb/abc", http.StatusFound)
	}))
	defer proxySrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	allowNZBFetch(c)

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	grabURL := "http://localhost:" + proxyURL.Port() + "/3/download?apikey=k&link=abc"

	_, err = c.AddURL(context.Background(), grabURL, "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"newznab error 203",
		"not allowed to download NZBs",
		`redirected from "localhost" to "127.0.0.1"`,
		"approved applications",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

// TestAddURL_DirectIndexerErrorHasNoRedirectGuidance pins the negative: a
// plain same-host failure keeps the structured newznab detail but must not
// speculate about proxies or redirects.
func TestAddURL_DirectIndexerErrorHasNoRedirectGuidance(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(nzbfinder203Body))
	}))
	defer indexerSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "newznab error 203") {
		t.Errorf("error missing structured newznab detail:\n%s", msg)
	}
	if strings.Contains(msg, "redirected") || strings.Contains(msg, "Prowlarr") {
		t.Errorf("same-host failure must not mention redirects/Prowlarr:\n%s", msg)
	}
}

// TestAddURL_NonNZBBodyIsRejectedBeforeSAB guards #2105. An indexer that
// answers a refused or rate-limited grab with HTTP 200 and an error page used
// to have that page forwarded to SAB, which refused to parse it and returned
// status:false — surfacing as "SABnzbd rejected download", blaming the only
// component that behaved correctly. The bad body must be rejected at the fetch
// step, and SAB must never see it.
func TestAddURL_NonNZBBodyIsRejectedBeforeSAB(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// Three prolog-legal lines then plain text: the exact shape that makes
		// SAB report `syntax error: line 4, column 0`.
		w.Write([]byte("\n\n\nYou have reached your download limit for today.\n"))
	}))
	defer indexerSrv.Close()

	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("SAB must not be called when the indexer returned a non-NZB body")
		json.NewEncoder(w).Encode(AddURLResponse{Status: true, NzoIDs: []string{"x"}})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	msg := err.Error()
	for _, want := range []string{"not an NZB", "HTTP 200", "reached your download limit"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "SABnzbd rejected") {
		t.Errorf("the error must not blame SAB for an indexer response:\n%s", msg)
	}
}

// TestAddURL_BodyReadFailureNamesTheIndexerFetch guards the second half of
// #2105: fetchNZBContent returned io.ReadAll's error unwrapped, so a failure
// part-way through the body arrived with no indication of what was being read
// — "context deadline exceeded (… while reading body)" and nothing else. The
// non-2xx branch one line above already says "fetch nzb from indexer".
func TestAddURL_BodyReadFailureNamesTheIndexerFetch(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Promise more than we deliver, then return: net/http closes the
		// connection and the client's body read fails short.
		w.Header().Set("Content-Length", "99999")
		w.Write([]byte("<?xml version=\"1.0\"?><nzb>"))
	}))
	defer indexerSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	if msg := err.Error(); !strings.Contains(msg, "fetch nzb from indexer") {
		t.Errorf("a body-read failure must name the indexer fetch:\n%s", msg)
	}
}

// TestAddURL_GzippedNZBReachesSABVerbatim pins the guarantee that makes the
// #2105 check safe: some indexers serve the NZB as a gzip file (Content-Type
// application/x-gzip, no Content-Encoding, so the transport does not unwrap
// it), SAB sniffs the magic and decompresses it itself, and validating must
// not disturb that. The body is unwrapped only to look at, and the original
// compressed bytes are what SAB receives.
func TestAddURL_GzippedNZBReachesSABVerbatim(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(testNZBContent)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Write(compressed.Bytes())
	}))
	defer indexerSrv.Close()

	var gotPayload []byte
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPayload = readMultipartFile(t, r)
		json.NewEncoder(w).Encode(AddURLResponse{Status: true, NzoIDs: []string{"nzo-gz"}})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	resp, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("a gzipped NZB must be accepted: %v", err)
	}
	if len(resp.NzoIDs) != 1 || resp.NzoIDs[0] != "nzo-gz" {
		t.Errorf("unexpected nzo ids: %v", resp.NzoIDs)
	}
	if !bytes.Equal(gotPayload, compressed.Bytes()) {
		t.Errorf("SAB must receive the original compressed bytes, got %d bytes vs %d sent", len(gotPayload), compressed.Len())
	}
}
