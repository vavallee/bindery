package sabnzbd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// dropFirstRequest returns a handler that kills the connection on the first
// request and serves body afterwards, which is the shape of the flake in
// #2157: the same release failed and then succeeded minutes apart.
func dropFirstRequest(t *testing.T, attempts *atomic.Int32, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close() // client sees the connection go away mid-request
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, body)
	}
}

// TestAddURL_RetriesATransientFetchFailure is #2157: one transient failure
// fetching the NZB used to fail the grab permanently — the download row went
// to failed and the book dropped back to Wanted with nothing to retry it.
func TestAddURL_RetriesATransientFetchFailure(t *testing.T) {
	var attempts atomic.Int32
	indexerSrv := httptest.NewServer(dropFirstRequest(t, &attempts, testNZBContent))
	defer indexerSrv.Close()

	var uploaded []byte
	sabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, uploaded = readMultipartFile(t, r)
		json.NewEncoder(w).Encode(AddURLResponse{Status: true, NzoIDs: []string{"nzo-retry"}})
	}))
	defer sabSrv.Close()

	c := New("127.0.0.1", 0, "testkey", "", false)
	c.baseURL = sabSrv.URL
	allowNZBFetch(c)

	resp, err := c.AddURL(context.Background(), indexerSrv.URL+"/getnzb/abc", "Test Book", "books", 0)
	if err != nil {
		t.Fatalf("a transient fetch failure must not fail the grab: %v", err)
	}
	if len(resp.NzoIDs) != 1 || resp.NzoIDs[0] != "nzo-retry" {
		t.Errorf("unexpected nzo ids: %v", resp.NzoIDs)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("indexer saw %d requests, want 2 (one dropped, one served)", got)
	}
	if string(uploaded) != testNZBContent {
		t.Errorf("SAB received %q, want the NZB from the successful attempt", uploaded)
	}
}

// An indexer refusing the grab means it just as firmly the third time, and
// each retry can cost a grab against a daily quota. The refusal must reach the
// caller after exactly one request.
func TestAddURL_DoesNotRetryAnIndexerRejection(t *testing.T) {
	var requests atomic.Int32
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/xml")
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
	if !strings.Contains(err.Error(), "newznab error 203") {
		t.Errorf("the indexer's reason must survive: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("indexer saw %d requests, want 1 — a refusal is not transient", got)
	}
}
