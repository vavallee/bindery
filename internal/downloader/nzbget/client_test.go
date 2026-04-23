package nzbget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// TestAdd verifies that Add sends the correct JSON-RPC request and returns the NZBID.
func TestAdd(t *testing.T) {
	var gotReq rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeRequest(t, r)
		json.NewEncoder(w).Encode(appendResponse{Result: 42})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "user", "pass", "", false)

	id, err := c.Add(context.Background(), "https://example.com/file.nzb", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id != 42 {
		t.Errorf("expected NZBID=42, got %d", id)
	}
	if gotReq.Method != "append" {
		t.Errorf("expected method=append, got %q", gotReq.Method)
	}
}

// TestAdd_Rejected verifies that Add returns an error when NZBGet rejects the download.
func TestAdd_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(appendResponse{Result: 0})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	c := New(host, port, "", "", "", false)

	_, err := c.Add(context.Background(), "https://example.com/bad.nzb", "Bad NZB", "books", 0)
	if err == nil {
		t.Fatal("expected error when NZBGet rejects download")
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
