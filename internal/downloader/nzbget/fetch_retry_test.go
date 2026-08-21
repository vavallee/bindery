package nzbget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestAdd_RetriesATransientFetchFailure is the NZBGet half of #2157. Both
// usenet clients fetch the NZB themselves, so both turned a momentary network
// failure into a permanently failed grab.
func TestAdd_RetriesATransientFetchFailure(t *testing.T) {
	var attempts atomic.Int32
	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZBContent)
	}))
	defer indexerSrv.Close()

	nzbgetSrv := nzbgetTestServer(t, []string{"books"}, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(appendResponse{Result: 77})
	})
	defer nzbgetSrv.Close()

	host, port := serverHostPort(t, nzbgetSrv.URL)
	c := New(host, port, "", "", "", false)
	allowNZBFetch(c)

	id, err := c.Add(context.Background(), indexerSrv.URL+"/getnzb/abc", "Book", "books", 0)
	if err != nil {
		t.Fatalf("a transient fetch failure must not fail the grab: %v", err)
	}
	if id != 77 {
		t.Errorf("got id %d, want 77", id)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("indexer saw %d requests, want 2 (one dropped, one served)", got)
	}
}
