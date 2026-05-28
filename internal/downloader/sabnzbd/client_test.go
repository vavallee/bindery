package sabnzbd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

const testNZBContent = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd"><nzb></nzb>`

// allowNZBFetch bypasses the SSRF guard for loopback test servers.
func allowNZBFetch(c *Client) {
	c.validateNZBURL = func(string) error { return nil }
}

// readMultipartFile decodes a multipart request body and returns the first
// uploaded file's filename and bytes. Asserts that there is exactly one file.
func readMultipartFile(t *testing.T, r *http.Request) (string, []byte) {
	t.Helper()
	ct := r.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("parse content-type %q: %v", ct, err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		t.Fatalf("read first part: %v", err)
	}
	defer part.Close()
	if got := part.FormName(); got != "name" {
		t.Errorf("multipart form field should be 'name', got %q", got)
	}
	data, err := io.ReadAll(part)
	if err != nil {
		t.Fatalf("read part body: %v", err)
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("expected exactly one multipart part, got err=%v", err)
	}
	return part.FileName(), data
}

// TestAddURL verifies that AddURL fetches the NZB from the indexer and submits
// its content to SAB via mode=addfile multipart upload — not by handing SAB
// the URL and expecting SAB to fetch it. The fetch-first shape is what makes
// SAB usable in containerised setups where only Bindery can reach the indexer.
func TestAddURL(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	var (
		gotMode     string
		gotCat      string
		gotNzbname  string
		gotFilename string
		gotPayload  []byte
		gotMethod   string
	)
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotMode = r.URL.Query().Get("mode")
		gotCat = r.URL.Query().Get("cat")
		gotNzbname = r.URL.Query().Get("nzbname")
		gotFilename, gotPayload = readMultipartFile(t, r)
		json.NewEncoder(w).Encode(AddURLResponse{
			Status: true,
			NzoIDs: []string{"SABnzbd_nzo_abc123"},
		})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	resp, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("add url: %v", err)
	}
	if !resp.Status {
		t.Error("expected status=true")
	}
	if len(resp.NzoIDs) != 1 || resp.NzoIDs[0] != "SABnzbd_nzo_abc123" {
		t.Errorf("unexpected nzo ids: %v", resp.NzoIDs)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST (multipart upload), got %s", gotMethod)
	}
	if gotMode != "addfile" {
		t.Errorf("expected mode=addfile, got %q", gotMode)
	}
	if gotCat != "books" {
		t.Errorf("expected cat=books, got %q", gotCat)
	}
	if gotNzbname != "Test Book" {
		t.Errorf("expected nzbname=%q, got %q", "Test Book", gotNzbname)
	}
	if gotFilename != "Test Book.nzb" {
		t.Errorf("expected filename=%q, got %q", "Test Book.nzb", gotFilename)
	}
	if string(gotPayload) != testNZBContent {
		t.Errorf("uploaded payload mismatch:\n want: %q\n got:  %q", testNZBContent, string(gotPayload))
	}
}

// TestAddURL_DoesNotSendIndexerURL guards against regressing to mode=addurl.
// The indexer URL must never appear in either the query string or the body
// SAB receives — that was the bug; if SAB ever sees the URL it'll try to
// fetch it from its own container, which is the failure ibsfox reported.
func TestAddURL_DoesNotSendIndexerURL(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	var sawIndexerURL bool
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Query string scan
		if strings.Contains(r.URL.RawQuery, "indexer") || strings.Contains(r.URL.RawQuery, "http") {
			sawIndexerURL = true
		}
		// Body scan (read fully then check)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), indexerSrv.URL) {
			sawIndexerURL = true
		}
		json.NewEncoder(w).Encode(AddURLResponse{Status: true, NzoIDs: []string{"x"}})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	if _, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "books", 0); err != nil {
		t.Fatalf("add url: %v", err)
	}
	if sawIndexerURL {
		t.Error("SAB must never see the indexer URL — submission has to be the NZB content")
	}
}

// TestAddURL_FetchFailure verifies that when Bindery can't fetch the NZB from
// the indexer, AddURL fails at the fetch step with a clear error and never
// hits SAB. Mirrors the equivalent NZBGet test.
func TestAddURL_FetchFailure(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer indexerSrv.Close()

	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("SAB must not be called when NZB fetch fails")
		json.NewEncoder(w).Encode(AddURLResponse{Status: true})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	_, err := c.AddURL(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "books", 0)
	if err == nil {
		t.Fatal("expected error on fetch failure")
		return
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 in error, got: %v", err)
	}
}

// TestAddURL_SSRFBlocked verifies the validateNZBURL guard rejects
// loopback/private URLs in production (i.e. when the test bypass is NOT
// applied). The SAB server is a tripwire that should never be hit.
func TestAddURL_SSRFBlocked(t *testing.T) {
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("SAB must not be called when SSRF guard blocks the indexer URL")
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	// Do NOT call allowNZBFetch — the guard must be active.

	_, err := c.AddURL(context.Background(), "http://127.0.0.1:9999/file.nzb", "Book", "books", 0)
	if err == nil {
		t.Fatal("expected SSRF guard to block loopback URL")
		return
	}
	if !strings.Contains(err.Error(), "url not allowed") {
		t.Errorf("expected 'url not allowed' in error, got: %v", err)
	}
}

// TestNZBFilename verifies the title→filename mapping strips path separators
// and falls back to "bindery.nzb" on empty.
func TestNZBFilename(t *testing.T) {
	cases := map[string]string{
		"Hello World":             "Hello World.nzb",
		"with/slash":              "with_slash.nzb",
		"with\\backslash":         "with_backslash.nzb",
		"with\x00null":            "with_null.nzb",
		"   ":                     "bindery.nzb",
		"":                        "bindery.nzb",
		"normal Title (2023)":     "normal Title (2023).nzb",
		"path/sep\\both\x00bytes": "path_sep_both_bytes.nzb",
	}
	for in, want := range cases {
		if got := nzbFilename(in); got != want {
			t.Errorf("nzbFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(QueueResponse{
			Queue: QueueData{
				Status: "Downloading",
				Speed:  "5.2 M",
				Slots: []QueueSlot{
					{
						NzoID:      "SABnzbd_nzo_abc123",
						Filename:   "Test Book",
						Status:     "Downloading",
						Category:   "books",
						MB:         "100.0",
						MBLeft:     "50.0",
						Percentage: "50",
						TimeLeft:   "0:01:00",
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	queue, err := c.GetQueue(context.Background())
	if err != nil {
		t.Fatalf("get queue: %v", err)
	}
	if queue.Status != "Downloading" {
		t.Errorf("expected status Downloading, got %s", queue.Status)
	}
	if len(queue.Slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(queue.Slots))
	}
	if queue.Slots[0].Percentage != "50" {
		t.Errorf("expected 50%%, got %s", queue.Slots[0].Percentage)
	}
}

func TestGetHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HistoryResponse{
			History: HistoryData{
				TotalSize: "5 GB",
				Slots: []HistorySlot{
					{
						NzoID:    "SABnzbd_nzo_def456",
						Name:     "Completed Book",
						Status:   "Completed",
						Category: "books",
						Size:     "5.2 MB",
						Path:     "/downloads/complete/books/Completed Book",
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	history, err := c.GetHistory(context.Background(), "books", 20)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(history.Slots) != 1 {
		t.Fatalf("expected 1 history slot, got %d", len(history.Slots))
	}
	if history.Slots[0].Status != "Completed" {
		t.Errorf("expected Completed, got %s", history.Slots[0].Status)
	}
}

func TestGetCategories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CategoriesResponse{
			Categories: []string{"*", "books", "movies", "tv"},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	cats, err := c.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("get categories: %v", err)
	}
	if len(cats) != 4 {
		t.Errorf("expected 4 categories, got %d", len(cats))
	}
}

func TestDeleteHistory(t *testing.T) {
	var gotMode, gotName, gotValue, gotDelFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotMode = q.Get("mode")
		gotName = q.Get("name")
		gotValue = q.Get("value")
		gotDelFiles = q.Get("del_files")
		json.NewEncoder(w).Encode(SimpleResponse{Status: true})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	if err := c.DeleteHistory(context.Background(), "SABnzbd_nzo_def456", false); err != nil {
		t.Fatalf("delete history: %v", err)
	}
	if gotMode != "history" || gotName != "delete" || gotValue != "SABnzbd_nzo_def456" {
		t.Errorf("unexpected params: mode=%s name=%s value=%s", gotMode, gotName, gotValue)
	}
	if gotDelFiles != "" {
		t.Errorf("del_files should be unset when deleteFiles=false, got %q", gotDelFiles)
	}

	if err := c.DeleteHistory(context.Background(), "nzo_xyz", true); err != nil {
		t.Fatalf("delete history with files: %v", err)
	}
	if gotDelFiles != "1" {
		t.Errorf("del_files should be 1 when deleteFiles=true, got %q", gotDelFiles)
	}
}

func TestTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CategoriesResponse{
			Categories: []string{"*", "books"},
		})
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	err := c.Test(context.Background())
	if err != nil {
		t.Errorf("test should pass: %v", err)
	}
}

// roundTripFunc is a test helper that implements http.RoundTripper via a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestTest_DNSNotFound verifies that a DNS lookup failure appends the Docker
// network hint.
func TestTest_DNSNotFound(t *testing.T) {
	dnsErr := &net.DNSError{Name: "sabnzbd-container", IsNotFound: true}
	c := New("sabnzbd-container", 8080, "key", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial: %w", dnsErr)
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "same Docker network") {
		t.Errorf("expected Docker network hint, got: %q", msg)
	}
}

// TestTest_ConnectionRefused verifies that ECONNREFUSED appends the port hint.
func TestTest_ConnectionRefused(t *testing.T) {
	c := New("127.0.0.1", 8080, "key", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "host firewall is rejecting") {
		t.Errorf("expected port hint, got: %q", msg)
	}
}

// TestTest_Timeout verifies that a timeout error appends the firewall hint.
func TestTest_Timeout(t *testing.T) {
	c := New("10.0.0.1", 8080, "key", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &netTimeoutErr{}
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "firewall or proxy") {
		t.Errorf("expected firewall hint, got: %q", msg)
	}
}

// TestTest_ServerError verifies that a clean HTTP 500 does NOT append any hint.
func TestTest_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = srv.URL

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
		return
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "host firewall is rejecting", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("clean server error must not produce hint %q; got: %q", hint, msg)
		}
	}
}

// netTimeoutErr is a minimal net.Error that signals a timeout.
type netTimeoutErr struct{}

func (e *netTimeoutErr) Error() string   { return "i/o timeout" }
func (e *netTimeoutErr) Timeout() bool   { return true }
func (e *netTimeoutErr) Temporary() bool { return true }
