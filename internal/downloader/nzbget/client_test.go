package nzbget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// configWithCategories returns a configResponse JSON payload exposing the
// given category names as Category1.Name, Category2.Name, … entries. Used to
// satisfy the preflight call Add() makes before append.
func configWithCategories(names ...string) configResponse {
	entries := make([]configEntry, 0, len(names))
	for i, n := range names {
		entries = append(entries, configEntry{
			Name:  fmt.Sprintf("Category%d.Name", i+1),
			Value: n,
		})
	}
	return configResponse{Result: entries}
}

// nzbgetTestServer returns a server that routes by JSON-RPC method:
// "config" responds with configWithCategories(cats...), "append" calls the
// supplied handler, and anything else 500s.
func nzbgetTestServer(t *testing.T, cats []string, append http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe rpcRequest
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			t.Fatalf("decode probe: %v", err)
		}
		switch probe.Method {
		case "config":
			json.NewEncoder(w).Encode(configWithCategories(cats...))
		case "append":
			// rewind the body so append handler can decode it itself
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			append(w, r)
		default:
			http.Error(w, "unexpected method "+probe.Method, http.StatusInternalServerError)
		}
	}))
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
	nzbgetSrv := nzbgetTestServer(t, []string{"books"}, func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(appendResponse{Result: 42})
	})
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

// TestAdd_Rejected verifies that when append still returns id 0 after the
// preflight has cleared the category, Add surfaces an actionable error that
// points the user at NZBGet's log and enumerates the likely causes.
func TestAdd_Rejected(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	nzbgetSrv := nzbgetTestServer(t, []string{"books"}, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(appendResponse{Result: 0})
	})
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	_, err := c.Add(context.Background(), indexerSrv.URL+"/bad.nzb", "Bad NZB", "books", 0)
	if err == nil {
		t.Fatal("expected error when NZBGet rejects download")
		return
	}
	for _, want := range []string{"append returned id 0", "NZBGet's log", "disk full"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %q", want, err.Error())
		}
	}
}

// TestAdd_UnknownCategory verifies that the preflight rejects a category that
// isn't defined in NZBGet, listing both the missing and the existing names so
// the user can fix either side. append must not be called in this path.
func TestAdd_UnknownCategory(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	nzbgetSrv := nzbgetTestServer(t, []string{"books", "movies"}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("append must not be called when preflight rejects the category")
		json.NewEncoder(w).Encode(appendResponse{Result: 1})
	})
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	_, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "audiobooks", 0)
	if err == nil {
		t.Fatal("expected error when category isn't configured in NZBGet")
		return
	}
	for _, want := range []string{`"audiobooks"`, `"books"`, `"movies"`, "Settings → Categories"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %q", want, err.Error())
		}
	}
}

// TestAdd_EmptyCategory verifies that when no category is set, the preflight
// is skipped entirely (no config RPC) and append proceeds with category="".
// NZBGet treats an empty category as "use the default destination".
func TestAdd_EmptyCategory(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	var methods []string
	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		methods = append(methods, req.Method)
		json.NewEncoder(w).Encode(appendResponse{Result: 7})
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	id, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "", 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != 7 {
		t.Errorf("expected id=7, got %d", id)
	}
	if len(methods) != 1 || methods[0] != "append" {
		t.Errorf("expected exactly one append call, got %v", methods)
	}
}

// TestAdd_PreflightUnreachable verifies that when the config RPC fails (e.g.
// older NZBGet, ControlIP restriction), Add doesn't pretend the category is
// wrong — it falls through to append and lets that response stand. The
// existing rejection-message path covers the case where append also fails.
func TestAdd_PreflightUnreachable(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	nzbgetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch req.Method {
		case "config":
			http.Error(w, "method not found", http.StatusNotFound)
		case "append":
			json.NewEncoder(w).Encode(appendResponse{Result: 99})
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	id, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Book", "anything", 0)
	if err != nil {
		t.Fatalf("Add should fall through to append when preflight fails: %v", err)
	}
	if id != 99 {
		t.Errorf("expected id=99, got %d", id)
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
		return
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
		return
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
		return
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
		return
	}
	if !strings.Contains(err.Error(), "check the port") {
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
		return
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
		return
	}
	msg := err.Error()
	for _, hint := range []string{"Docker network", "check the port", "firewall or proxy"} {
		if strings.Contains(msg, hint) {
			t.Errorf("clean server error must not produce hint %q; got: %q", hint, msg)
		}
	}
}

// TestListCategories_Parses verifies CategoryN.Name entries are extracted in
// order and that non-Name entries (DestDir/Unpack/PostScript) and empty
// Values are filtered out.
func TestListCategories_Parses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.Method != "config" {
			t.Errorf("expected method=config, got %q", req.Method)
		}
		json.NewEncoder(w).Encode(configResponse{
			Result: []configEntry{
				{Name: "MainDir", Value: "/downloads"},
				{Name: "Category1.Name", Value: "Books"},
				{Name: "Category1.DestDir", Value: "/downloads/books"},
				{Name: "Category1.Unpack", Value: "yes"},
				{Name: "Category2.Name", Value: "Audiobooks"},
				{Name: "Category2.Aliases", Value: ""},
				{Name: "Category3.Name", Value: ""}, // empty — filtered
				{Name: "Category10.Name", Value: "Movies"},
				{Name: "DupeCheck", Value: "yes"},
			},
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	cats, err := c.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	want := []string{"Books", "Audiobooks", "Movies"}
	if len(cats) != len(want) {
		t.Fatalf("expected %d categories, got %d (%v)", len(want), len(cats), cats)
	}
	for i, w := range want {
		if cats[i] != w {
			t.Errorf("cats[%d]: want %q, got %q", i, w, cats[i])
		}
	}
}

// TestCheckCategories_Match verifies the common case: every wanted category
// is present, no error returned.
func TestCheckCategories_Match(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(configWithCategories("Books", "Audiobooks"))
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	if err := c.CheckCategories(context.Background(), "Books", "Audiobooks"); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// TestCheckCategories_EmptySkipped verifies that all-empty wanted entries
// short-circuit and don't hit the RPC at all (important: an unconfigured
// Bindery client shouldn't gratuitously fail Test against a working NZBGet).
func TestCheckCategories_EmptySkipped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	if err := c.CheckCategories(context.Background(), "", ""); err != nil {
		t.Errorf("expected nil error for all-empty wanted, got: %v", err)
	}
	if called {
		t.Error("CheckCategories must not call config RPC when all wanted are empty")
	}
}

// TestCheckCategories_OneMissing verifies the single-missing case produces a
// singular "no category" message that names both sides.
func TestCheckCategories_OneMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(configWithCategories("Books"))
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.CheckCategories(context.Background(), "Books", "Audiobooks")
	if err == nil {
		t.Fatal("expected error when one category is missing")
		return
	}
	for _, want := range []string{`no category "Audiobooks"`, `"Books"`, "Settings → Categories"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %q", want, err.Error())
		}
	}
}

// TestCheckCategories_MultipleMissing verifies the plural variant fires when
// both wanted categories are absent.
func TestCheckCategories_MultipleMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(configWithCategories("Movies"))
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.CheckCategories(context.Background(), "Books", "Audiobooks")
	if err == nil {
		t.Fatal("expected error when both categories are missing")
		return
	}
	for _, want := range []string{"no categories", `"Books"`, `"Audiobooks"`, `"Movies"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %q", want, err.Error())
		}
	}
}

// TestCheckCategories_NoneDefined verifies that when NZBGet has zero
// categories configured, the error message says "none defined" rather than
// listing an empty set.
func TestCheckCategories_NoneDefined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(configResponse{Result: []configEntry{
			{Name: "MainDir", Value: "/downloads"},
		}})
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.CheckCategories(context.Background(), "Books")
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "none defined") {
		t.Errorf("expected 'none defined' in error, got: %q", err.Error())
	}
}

// TestCheckCategories_ListFails verifies the list-RPC error is surfaced
// directly (so the adapter's Test path can show it to the user).
func TestCheckCategories_ListFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	err := c.CheckCategories(context.Background(), "Books")
	if err == nil {
		t.Fatal("expected error from list-RPC failure")
		return
	}
	if !strings.Contains(err.Error(), "list nzbget categories") {
		t.Errorf("expected wrapped list error, got: %q", err.Error())
	}
}

// TestRPCRequest_ParamsMarshalledLast guards the NZBGet JSON-RPC quirk that
// forced this fix: NZBGet's param reader overshoots one byte past the final
// array element, so anything after the params array (e.g. an "id" field) gets
// misread as a post-processing parameter and the append is rejected with
// "Invalid parameter (Parameters)". The envelope must therefore end with the
// params array immediately before the closing brace.
func TestRPCRequest_ParamsMarshalledLast(t *testing.T) {
	buf, err := json.Marshal(rpcRequest{
		Method: "append",
		ID:     1,
		Params: []any{"name", "content", "books", 0, false, false, "", 0, "FORCE"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(buf)
	if !strings.HasSuffix(body, `]}`) {
		t.Errorf("request must end with the params array then '}'; got: %s", body)
	}
	if strings.Index(body, `"params"`) < strings.Index(body, `"id"`) {
		t.Errorf(`"params" must be marshalled after "id"; got: %s`, body)
	}
}

// TestAppendParams_Shape verifies the append RPC is called with exactly the
// nine version-agnostic parameters in the order NZBGet expects, with the
// correct JSON types at each position. This is the regression guard for the
// original bug, where positions 5–8 carried the wrong types (e.g. addPaused
// got "" instead of a bool), causing NZBGet to silently reject the download
// with id 0 on every version.
func TestAppendParams_Shape(t *testing.T) {
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	var gotReq rpcRequest
	nzbgetSrv := nzbgetTestServer(t, []string{"books"}, func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(appendResponse{Result: 99})
	})
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "user", "pass", "", false)
	allowNZBFetch(c)

	_, err := c.Add(context.Background(), indexerSrv.URL+"/file.nzb", "Test Book", "books", 7)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(gotReq.Params) != 9 {
		t.Fatalf("append expects 9 params, got %d: %v", len(gotReq.Params), gotReq.Params)
	}
	// Positions and types per NZBGet's append signature:
	// [name, content, category, priority, addToTop, addPaused, dupeKey, dupeScore, dupeMode].
	// JSON numbers decode into float64, so check the numeric positions as such.
	assertString := func(i int, want string) {
		if got, ok := gotReq.Params[i].(string); !ok || got != want {
			t.Errorf("param[%d]: want string %q, got %T %v", i, want, gotReq.Params[i], gotReq.Params[i])
		}
	}
	assertBool := func(i int, want bool) {
		if got, ok := gotReq.Params[i].(bool); !ok || got != want {
			t.Errorf("param[%d]: want bool %v, got %T %v", i, want, gotReq.Params[i], gotReq.Params[i])
		}
	}
	assertNumber := func(i int, want float64) {
		if got, ok := gotReq.Params[i].(float64); !ok || got != want {
			t.Errorf("param[%d]: want number %v, got %T %v", i, want, gotReq.Params[i], gotReq.Params[i])
		}
	}
	assertString(0, "Test Book") // name
	// param[1] is base64 content — asserted in TestAdd
	assertString(2, "books") // category
	assertNumber(3, 7)       // priority
	assertBool(4, false)     // addToTop
	assertBool(5, false)     // addPaused
	assertString(6, "")      // dupeKey
	assertNumber(7, 0)       // dupeScore
	assertString(8, "FORCE") // dupeMode
}
