package nzbget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// serverHostPort parses a test server URL into host and port.
func serverHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return host, port
}

// decodeRequest parses the incoming JSON-RPC body and returns the method name.
func decodeRequest(t *testing.T, r *http.Request) rpcRequest {
	t.Helper()
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode rpc request: %v", err)
	}
	return req
}

// TestTest_Success verifies that Test passes when NZBGet returns a version string.
func TestTest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Method != "version" {
			t.Errorf("expected method=version, got %q", req.Method)
		}
		json.NewEncoder(w).Encode(versionResponse{Result: "21.1"})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "user", "pass", "", false)

	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test should pass: %v", err)
	}
}

// TestTest_AuthFailure verifies that Test returns an error on HTTP 401.
func TestTest_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "bad", "creds", "", false)

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error on auth failure")
	}
}

// TestTest_EmptyVersion verifies that Test returns an error when NZBGet returns
// an empty result (e.g. misconfigured or wrong endpoint).
func TestTest_EmptyVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(versionResponse{Result: ""})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error on empty version")
	}
}

const testNZBContent = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd"><nzb></nzb>`

// allowNZBFetch bypasses the SSRF guard for loopback test servers.
func allowNZBFetch(c *Client) {
	c.validateNZBURL = func(string) error { return nil }
}

// TestAdd verifies that Add fetches NZB content from the indexer and submits it
// as base64 to NZBGet's append RPC (rather than sending a URL for NZBGet to
// fetch itself, which fails when NZBGet can't reach the indexer).
func TestAdd(t *testing.T) {
	// indexerSrv serves the NZB file, simulating a Prowlarr proxy download URL.
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	var gotReq rpcRequest
	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(appendResponse{Result: 42})
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "user", "pass", "", false)
	allowNZBFetch(c)

	id, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != 42 {
		t.Errorf("expected NZBID=42, got %d", id)
	}
	if gotReq.Method != "append" {
		t.Errorf("expected method=append, got %q", gotReq.Method)
	}
	// Verify content was sent as base64, not a URL.
	if len(gotReq.Params) < 2 {
		t.Fatal("expected at least 2 params in append request")
	}
	raw, ok := gotReq.Params[1].(string)
	if !ok {
		t.Fatalf("param[1] is not a string: %T", gotReq.Params[1])
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		t.Errorf("Add must send base64 content, not a URL; got %q", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("param[1] is not valid base64: %v", err)
	}
	if string(decoded) != testNZBContent {
		t.Errorf("NZB content mismatch: got %q, want %q", string(decoded), testNZBContent)
	}
}

// TestAdd_Rejected verifies that Add returns an error when NZBGet rejects the download.
func TestAdd_Rejected(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(appendResponse{Result: 0})
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	_, err := c.Add(context.Background(), indexerSrv.URL+"/bad.nzb", "Bad NZB", "books", 0)
	if err == nil {
		t.Fatal("expected error when NZBGet rejects download")
	}
}

// TestAdd_FetchFailure verifies that Add returns a clear error when the NZB
// cannot be fetched from the indexer (e.g. auth failure), rather than sending
// a URL for NZBGet to fail on silently.
func TestAdd_FetchFailure(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer indexerSrv.Close()

	// NZBGet should never be called — Add must fail at the fetch step.
	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("NZBGet append must not be called when NZB fetch fails")
		json.NewEncoder(w).Encode(appendResponse{Result: 1})
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	_, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "books", 0)
	if err == nil {
		t.Fatal("expected error on fetch failure")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 in error, got: %v", err)
	}
}

// TestAdd_SSRFBlocked verifies that fetchNZBContent rejects loopback/private URLs
// when the SSRF guard is active (i.e. not bypassed for tests).
func TestAdd_SSRFBlocked(t *testing.T) {
	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("NZBGet must not be called when SSRF guard blocks the indexer URL")
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	// Do NOT call allowNZBFetch — the guard must be active.

	_, err := c.Add(context.Background(), "http://127.0.0.1:9999/file.nzb", "Book", "books", 0)
	if err == nil {
		t.Fatal("expected SSRF guard to block loopback URL")
	}
	if !strings.Contains(err.Error(), "url not allowed") {
		t.Errorf("expected 'url not allowed' in error, got: %v", err)
	}
}

// TestGetQueue verifies that GetQueue parses active download groups.
func TestGetQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Method != "listgroups" {
			t.Errorf("expected method=listgroups, got %q", req.Method)
		}
		json.NewEncoder(w).Encode(listGroupsResponse{
			Result: []Group{
				{
					NZBID:           101,
					NZBName:         "Interesting Book",
					Status:          "DOWNLOADING",
					Category:        "books",
					FileSizeMB:      250.0,
					RemainingSizeMB: 125.0,
					ActiveDownloads: 4,
				},
			},
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	groups, err := c.GetQueue(context.Background())
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].NZBID != 101 {
		t.Errorf("expected NZBID=101, got %d", groups[0].NZBID)
	}
	if groups[0].Status != "DOWNLOADING" {
		t.Errorf("expected status DOWNLOADING, got %q", groups[0].Status)
	}
	if groups[0].ActiveDownloads != 4 {
		t.Errorf("expected 4 active downloads, got %d", groups[0].ActiveDownloads)
	}
}

// TestGetHistory verifies that GetHistory returns success and failure items.
func TestGetHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Method != "history" {
			t.Errorf("expected method=history, got %q", req.Method)
		}
		json.NewEncoder(w).Encode(historyResponse{
			Result: []HistoryItem{
				{
					NZBID:      201,
					NZBName:    "Good Book",
					Status:     "SUCCESS/ALL",
					Category:   "books",
					FileSizeMB: 300.0,
					DestDir:    "/downloads/complete/books/Good Book",
				},
				{
					NZBID:    202,
					NZBName:  "Bad Book",
					Status:   "FAILURE/UNPACK",
					Category: "books",
				},
			},
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	items, err := c.GetHistory(context.Background())
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(items))
	}
	if !IsSuccess(items[0].Status) {
		t.Errorf("expected SUCCESS status for item 0, got %q", items[0].Status)
	}
	if !IsFailure(items[1].Status) {
		t.Errorf("expected FAILURE status for item 1, got %q", items[1].Status)
	}
}

// TestRemove verifies that Remove sends the DeleteFinal editqueue command.
func TestRemove(t *testing.T) {
	var gotReq rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(editQueueResponse{Result: true})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	if err := c.Remove(context.Background(), 101); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gotReq.Method != "editqueue" {
		t.Errorf("expected method=editqueue, got %q", gotReq.Method)
	}
	// Verify first param is "DeleteFinal"
	if len(gotReq.Params) == 0 {
		t.Fatal("expected params in editqueue request")
	}
	if gotReq.Params[0] != "DeleteFinal" {
		t.Errorf("expected DeleteFinal command, got %v", gotReq.Params[0])
	}
}

// TestRemoveHistory verifies that RemoveHistory sends the HistoryDelete command.
func TestRemoveHistory(t *testing.T) {
	var gotReq rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(editQueueResponse{Result: true})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	if err := c.RemoveHistory(context.Background(), 202); err != nil {
		t.Fatalf("RemoveHistory: %v", err)
	}
	if gotReq.Params[0] != "HistoryDelete" {
		t.Errorf("expected HistoryDelete command, got %v", gotReq.Params[0])
	}
}

// TestParseNZBID verifies ID parsing.
func TestParseNZBID(t *testing.T) {
	id, err := ParseNZBID("42")
	if err != nil || id != 42 {
		t.Fatalf("ParseNZBID: got id=%d err=%v", id, err)
	}
	if _, err := ParseNZBID("abc"); err == nil {
		t.Fatal("expected error for non-numeric ID")
	}
}

// TestIsSuccess and TestIsFailure verify the status helper functions.
func TestIsSuccess(t *testing.T) {
	cases := []struct {
		status  string
		success bool
	}{
		{"SUCCESS/ALL", true},
		{"SUCCESS", true},
		{"FAILURE/UNPACK", false},
		{"DELETED", false},
		{"DOWNLOADING", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSuccess(tc.status); got != tc.success {
			t.Errorf("IsSuccess(%q) = %v, want %v", tc.status, got, tc.success)
		}
	}
}

func TestIsFailure(t *testing.T) {
	cases := []struct {
		status  string
		failure bool
	}{
		{"FAILURE/UNPACK", true},
		{"FAILURE/PAR", true},
		{"DELETED", true},
		{"SUCCESS/ALL", false},
		{"DOWNLOADING", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsFailure(tc.status); got != tc.failure {
			t.Errorf("IsFailure(%q) = %v, want %v", tc.status, got, tc.failure)
		}
	}
}

// TestBasicAuth verifies that the client sends HTTP Basic auth credentials.
func TestBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		json.NewEncoder(w).Encode(versionResponse{Result: "21.0"})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "admin", "secret", "", false)

	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if gotUser != "admin" || gotPass != "secret" {
		t.Errorf("expected admin/secret, got %q/%q", gotUser, gotPass)
	}
}

// roundTripFunc is a test helper that implements http.RoundTripper via a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// netTimeoutErr is a minimal net.Error that signals a timeout.
type netTimeoutErr struct{}

func (e *netTimeoutErr) Error() string   { return "i/o timeout" }
func (e *netTimeoutErr) Timeout() bool   { return true }
func (e *netTimeoutErr) Temporary() bool { return true }

// TestTest_DNSNotFound verifies that a DNS lookup failure appends the Docker
// network hint.
func TestTest_DNSNotFound(t *testing.T) {
	dnsErr := &net.DNSError{Name: "nzbget-container", IsNotFound: true}
	c := New("nzbget-container", 6789, "u", "p", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial: %w", dnsErr)
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "same Docker network") {
		t.Errorf("expected Docker network hint, got: %q", err.Error())
	}
}

// TestTest_ConnectionRefused verifies that ECONNREFUSED appends the port hint.
func TestTest_ConnectionRefused(t *testing.T) {
	c := New("127.0.0.1", 6789, "u", "p", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "host firewall is rejecting") {
		t.Errorf("expected port hint, got: %q", err.Error())
	}
}

// TestTest_Timeout verifies that a timeout error appends the firewall hint.
func TestTest_Timeout(t *testing.T) {
	c := New("10.0.0.1", 6789, "u", "p", "", false)
	c.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &netTimeoutErr{}
		}),
	}

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "firewall or proxy") {
		t.Errorf("expected firewall hint, got: %q", err.Error())
	}
}

// TestTest_ServerError_NoHint verifies that a clean HTTP 500 does NOT append
// any network hint (it's a server-side error, not a transport failure).
func TestTest_ServerError_NoHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "host firewall is rejecting", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("clean server error must not produce hint %q; got: %q", hint, msg)
		}
	}
}
