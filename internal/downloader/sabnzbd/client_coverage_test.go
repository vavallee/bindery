package sabnzbd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestNew_SSLAndURLBase(t *testing.T) {
	c := New("sab.local", 8443, "k", "sabnzbd/", true)
	if c.baseURL != "https://sab.local:8443/sabnzbd" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

// fakeSAB answers get_config for the misc and categories sections and records
// each query so tests can assert Bindery never asks for the unfiltered config
// (which carries news-server passwords).
type fakeSAB struct {
	*httptest.Server
	mu      sync.Mutex
	queries []url.Values
	misc    string // JSON body for section=misc
	cats    string // JSON body for section=categories
	status  int
}

func newFakeSAB(t *testing.T, misc, cats string) *fakeSAB {
	t.Helper()
	f := &fakeSAB{misc: misc, cats: cats, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.queries = append(f.queries, q)
		status := f.status
		f.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, "API Key Incorrect", status)
			return
		}
		switch q.Get("section") {
		case "misc":
			fmt.Fprint(w, f.misc)
		case "categories":
			fmt.Fprint(w, f.cats)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSAB) client() *Client {
	c := New("127.0.0.1", 0, "full-key", "", false)
	c.baseURL = f.URL
	return c
}

func TestCompleteDir(t *testing.T) {
	const miscAbs = `{"config":{"misc":{"complete_dir":"/downloads/complete"}}}`
	cases := []struct {
		name     string
		misc     string
		cats     string
		category string
		wantDir  string
		wantOwn  bool
	}{
		{"no category", miscAbs, "", "", "/downloads/complete", false},
		{"relative category folder", miscAbs,
			`{"config":{"categories":[{"name":"other","dir":"x"},{"name":"Books","dir":"books"}]}}`,
			"books", "/downloads/complete/books", true},
		{"absolute category folder", miscAbs,
			`{"config":{"categories":[{"name":"books","dir":"/srv/books*"}]}}`,
			"books", "/srv/books", true},
		{"category without its own folder", miscAbs,
			`{"config":{"categories":[{"name":"books","dir":""}]}}`,
			"books", "/downloads/complete", false},
		{"unknown category answers status false", miscAbs,
			`{"status":false,"error":"keyword not found"}`,
			"books", "/downloads/complete", false},
		{"malformed category reply ignored", miscAbs, `not json`,
			"books", "/downloads/complete", false},
		{"relative complete_dir cannot be resolved",
			`{"config":{"misc":{"complete_dir":"Downloads/complete"}}}`,
			`{"config":{"categories":[{"name":"books","dir":"books"}]}}`,
			"books", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSAB(t, tc.misc, tc.cats)
			dir, own, err := f.client().CompleteDir(context.Background(), tc.category)
			if err != nil {
				t.Fatalf("CompleteDir: %v", err)
			}
			if dir != tc.wantDir || own != tc.wantOwn {
				t.Errorf("CompleteDir = (%q, %v), want (%q, %v)", dir, own, tc.wantDir, tc.wantOwn)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, q := range f.queries {
				if q.Get("mode") != "get_config" || q.Get("section") == "" || q.Get("keyword") == "" {
					t.Errorf("get_config must be scoped to a section and keyword, got %v", q)
				}
				if q.Get("apikey") != "full-key" {
					t.Errorf("apikey = %q", q.Get("apikey"))
				}
			}
			if tc.category != "" {
				last := f.queries[len(f.queries)-1]
				if last.Get("section") != "categories" || last.Get("keyword") != tc.category {
					t.Errorf("category lookup = %v", last)
				}
			}
		})
	}
}

func TestCompleteDir_Errors(t *testing.T) {
	t.Run("nzb-only key refused", func(t *testing.T) {
		f := newFakeSAB(t, `{"status":false,"error":" API Key Incorrect "}`, "")
		_, _, err := f.client().CompleteDir(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "refused get_config: API Key Incorrect") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("HTTP 403", func(t *testing.T) {
		f := newFakeSAB(t, "", "")
		f.status = http.StatusForbidden
		_, _, err := f.client().CompleteDir(context.Background(), "books")
		if err == nil || !strings.Contains(err.Error(), "read complete_dir") || !strings.Contains(err.Error(), "403") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "full-key") {
			t.Errorf("error leaks the API key: %v", err)
		}
	})
	t.Run("malformed misc reply", func(t *testing.T) {
		f := newFakeSAB(t, `{"config":`, "")
		if _, _, err := f.client().CompleteDir(context.Background(), ""); err == nil {
			t.Fatal("want a decode error")
		}
	})
}

// failingTransport fails every request with err, standing in for a refused
// connection without touching the network.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestAPICall_ErrorPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New("127.0.0.1", 0, "secretkey", "", false)
	c.baseURL = srv.URL
	ctx := context.Background()

	calls := map[string]func() error{
		"pause":          func() error { return c.Pause(ctx, "n") },
		"resume":         func() error { return c.Resume(ctx, "n") },
		"delete":         func() error { return c.Delete(ctx, "n", true) },
		"delete history": func() error { return c.DeleteHistory(ctx, "n", true) },
		"history":        func() error { _, err := c.GetHistory(ctx, "books", 10); return err },
		"queue":          func() error { _, err := c.GetQueue(ctx); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "HTTP 500: boom") {
			t.Errorf("%s: err = %v, want the HTTP 500 body surfaced", name, err)
		}
	}

	// A base URL that cannot form a request fails before any I/O, and the
	// message must not carry the API key.
	c.baseURL = "http://bad host"
	err := c.Pause(ctx, "n")
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("err = %v, want a build-request error", err)
	}
	if strings.Contains(err.Error(), "secretkey") {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestDelete_SendsDelFiles(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_ = json.NewEncoder(w).Encode(SimpleResponse{Status: true})
	}))
	defer srv.Close()
	c := New("127.0.0.1", 0, "k", "", false)
	c.baseURL = srv.URL
	if err := c.Delete(context.Background(), "nzo_9", true); err != nil {
		t.Fatal(err)
	}
	if got.Get("mode") != "queue" || got.Get("name") != "delete" || got.Get("value") != "nzo_9" || got.Get("del_files") != "1" {
		t.Errorf("query = %v", got)
	}
}

func TestGetHistory_CategoryFilter(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"history":{"slots":[]}}`)
	}))
	defer srv.Close()
	c := New("127.0.0.1", 0, "k", "", false)
	c.baseURL = srv.URL
	if _, err := c.GetHistory(context.Background(), "books", 25); err != nil {
		t.Fatal(err)
	}
	if got.Get("cat") != "books" || got.Get("limit") != "25" || got.Get("mode") != "history" {
		t.Errorf("query = %v", got)
	}
}

// newAddURLPair returns an indexer serving a valid NZB and a SAB whose upload
// handler is h.
func newAddURLPair(t *testing.T, h http.HandlerFunc) (*Client, string) {
	t.Helper()
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	t.Cleanup(indexer.Close)
	sab := httptest.NewServer(h)
	t.Cleanup(sab.Close)
	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sab.URL
	allowNZBFetch(c)
	return c, indexer.URL + "/get.nzb"
}

func TestAddURL_UploadFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{"upload HTTP 401", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "API Key Required", http.StatusUnauthorized)
		}, "add nzb: HTTP 401: API Key Required"},
		{"rejected with reason", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"status":false,"error":"  duplicate NZB  "}`)
		}, "SABnzbd rejected download: duplicate NZB"},
		{"rejected without reason", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"status":false}`)
		}, "SABnzbd gave no reason"},
		{"malformed upload reply", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `<html>`)
		}, "add nzb:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, nzb := newAddURLPair(t, tc.handler)
			resp, err := c.AddURL(context.Background(), nzb, "Title", "books", 0)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AddURL = %+v, %v; want error containing %q", resp, err, tc.wantErr)
			}
		})
	}
}

func TestAddURL_UploadTransportErrorRedactsKey(t *testing.T) {
	c, nzb := newAddURLPair(t, func(http.ResponseWriter, *http.Request) {})
	c.http.Transport = failingTransport{err: errors.New("connection refused")}
	_, err := c.AddURL(context.Background(), nzb, "Title", "books", 0)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "testkey") {
		t.Errorf("upload error leaks the API key: %v", err)
	}
}

func TestAddURL_UploadBuildErrorRedactsKey(t *testing.T) {
	c, nzb := newAddURLPair(t, func(http.ResponseWriter, *http.Request) {})
	c.baseURL = "http://bad host"
	_, err := c.AddURL(context.Background(), nzb, "Title", "books", 0)
	if err == nil || !strings.Contains(err.Error(), "build upload") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "testkey") {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestValidateNZBFetchURL_DefaultPolicy(t *testing.T) {
	c := New("127.0.0.1", 0, "k", "", false)
	c.validateNZBURL = nil
	if err := c.validateNZBFetchURL("file:///etc/passwd"); err == nil {
		t.Error("default policy must reject a non-HTTP scheme")
	}
	if err := c.validateNZBFetchURL("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Error("default policy must reject the cloud metadata address")
	}
}

func TestCheckCategories_MultipleMissing(t *testing.T) {
	err := checkCategories(nil, []string{"books", "", "audiobooks"})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `categories "books", "audiobooks"`) || !strings.Contains(msg, "none defined") {
		t.Errorf("message = %q", msg)
	}
}
