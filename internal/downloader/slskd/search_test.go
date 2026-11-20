package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// searchFake is a slskd that serves the search endpoints.
type searchFake struct {
	mu         sync.Mutex
	posts      []searchRequest
	deletes    int
	cancels    int
	polls      int
	completeAt int    // poll count at which isComplete turns true; <0 never
	responses  string // JSON array for "responses"
	rawFinal   string // when set, the body of the includeResponses read
	postStatus int    // 0 means 200
	postDelay  time.Duration

	active, maxActive atomic.Int32
}

func (f *searchFake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/searches", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		n := f.active.Add(1)
		defer f.active.Add(-1)
		if n > f.maxActive.Load() {
			f.maxActive.Store(n)
		}
		if n > 1 {
			// What real slskd does with a second concurrent search.
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var req searchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.posts = append(f.posts, req)
		f.mu.Unlock()
		time.Sleep(f.postDelay)
		if f.postStatus != 0 {
			w.WriteHeader(f.postStatus)
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + req.ID + `","isComplete":false}`))
	})
	mux.HandleFunc("/api/v0/searches/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodDelete:
			f.deletes++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			f.cancels++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if r.URL.Query().Get("includeResponses") == "true" {
				if f.rawFinal != "" {
					_, _ = w.Write([]byte(f.rawFinal))
					return
				}
				_, _ = w.Write([]byte(`{"isComplete":true,"responses":` + f.responses + `}`))
				return
			}
			f.polls++
			done := f.completeAt >= 0 && f.polls >= f.completeAt
			_, _ = fmt.Fprintf(w, `{"isComplete":%t}`, done)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func newSearchClient(t *testing.T, f *searchFake) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(u.Hostname(), port, testKey, "", false)
	c.pollInterval = 5 * time.Millisecond
	c.searchTimeout = 200 * time.Millisecond
	c.searchGrace = 100 * time.Millisecond
	return c
}

const oneResponse = `[{"username":"peer1","hasFreeUploadSlot":true,"queueLength":0,"uploadSpeed":1048576,
 "files":[{"filename":"Books\\Dune.epub","size":1000,"extension":"epub"}]}]`

func TestSearchSuccess(t *testing.T) {
	f := &searchFake{completeAt: 2, responses: oneResponse}
	c := newSearchClient(t, f)
	got, err := c.Search(context.Background(), "dune herbert")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Username != "peer1" || !got[0].HasFreeUploadSlot || got[0].UploadSpeed != 1048576 {
		t.Fatalf("responses = %+v", got)
	}
	if len(got[0].Files) != 1 || got[0].Files[0].Filename != `Books\Dune.epub` || got[0].Files[0].Size != 1000 {
		t.Fatalf("files = %+v", got[0].Files)
	}
	if f.deletes != 1 {
		t.Errorf("search deleted %d times, want 1", f.deletes)
	}
	if f.cancels != 0 {
		t.Errorf("a completed search was cancelled")
	}
	p := f.posts[0]
	if p.SearchText != "dune herbert" || p.SearchTimeout != 200 || p.ResponseLimit != maxResponses || p.FileLimit != maxFiles || p.ID == "" {
		t.Errorf("post body = %+v", p)
	}
}

func TestSearchEmptyResults(t *testing.T) {
	f := &searchFake{completeAt: 1, responses: `[]`}
	c := newSearchClient(t, f)
	got, err := c.Search(context.Background(), "nothing here")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.deletes != 1 {
		t.Errorf("empty search not deleted")
	}
}

func TestSearchEmptyTextRefused(t *testing.T) {
	f := &searchFake{completeAt: 1, responses: `[]`}
	c := newSearchClient(t, f)
	if _, err := c.Search(context.Background(), "   "); err == nil {
		t.Fatal("empty text accepted")
	}
	if len(f.posts) != 0 {
		t.Error("an empty search reached slskd")
	}
}

func TestSearchTimeoutCancelsAndReadsWhatExists(t *testing.T) {
	f := &searchFake{completeAt: -1, responses: oneResponse}
	c := newSearchClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	got, err := c.Search(ctx, "slow search")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < c.searchTimeout {
		t.Error("returned before the search timeout")
	}
	if len(got) != 1 {
		t.Fatalf("partial responses = %v", got)
	}
	if f.cancels != 1 || f.deletes != 1 {
		t.Errorf("cancels=%d deletes=%d, want 1 and 1", f.cancels, f.deletes)
	}
}

func TestSearchContextCancelStillDeletes(t *testing.T) {
	f := &searchFake{completeAt: -1, responses: `[]`}
	c := newSearchClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := c.Search(ctx, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if f.deletes != 1 {
		t.Errorf("a cancelled search was left in slskd (deletes=%d)", f.deletes)
	}
}

func TestSearchSerialisesConcurrentCallers(t *testing.T) {
	f := &searchFake{completeAt: 1, responses: oneResponse, postDelay: 20 * time.Millisecond}
	c := newSearchClient(t, f)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Search(context.Background(), "same time")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a queued search failed: %v", err)
		}
	}
	if m := f.maxActive.Load(); m != 1 {
		t.Errorf("slskd saw %d concurrent POST /searches, want 1 (a second one is a 429)", m)
	}
	if len(f.posts) != 6 {
		t.Errorf("posts = %d, want 6", len(f.posts))
	}
}

func TestSearchBoundedWaitBehindAnotherSearch(t *testing.T) {
	f := &searchFake{completeAt: -1, responses: `[]`}
	c := newSearchClient(t, f)
	c.searchWait = 50 * time.Millisecond
	c.searchTimeout = 2 * time.Second
	started := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		close(started)
		_, _ = c.Search(ctx, "long one")
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Search(context.Background(), "impatient"); !errors.Is(err, ErrSearchBusy) {
		t.Fatalf("err = %v, want ErrSearchBusy", err)
	}
}

func TestSearch429IsBusy(t *testing.T) {
	f := &searchFake{completeAt: 1, postStatus: http.StatusTooManyRequests}
	c := newSearchClient(t, f)
	if _, err := c.Search(context.Background(), "busy"); !errors.Is(err, ErrSearchBusy) {
		t.Fatalf("err = %v, want ErrSearchBusy", err)
	}
	if f.deletes != 0 {
		t.Error("deleted a search that never started")
	}
}

func TestSearchRefusedKey(t *testing.T) {
	f := &searchFake{completeAt: 1}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(u.Hostname(), port, "wrong-key", "", false)
	_, err := c.Search(context.Background(), "x y")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 StatusError", err)
	}
	if strings.Contains(err.Error(), "wrong-key") {
		t.Error("the key leaked into the error")
	}
}

func TestSearchMalformedJSON(t *testing.T) {
	f := &searchFake{completeAt: 1, rawFinal: `<html>not slskd</html>`}
	c := newSearchClient(t, f)
	_, err := c.Search(context.Background(), "garbage")
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("err = %v", err)
	}
	if f.deletes != 1 {
		t.Error("search not deleted after a malformed answer")
	}
}

func TestSearchOversizedBody(t *testing.T) {
	big := `{"isComplete":true,"responses":[],"pad":"` + strings.Repeat("a", maxSearchBytes+10) + `"}`
	f := &searchFake{completeAt: 1, rawFinal: big}
	c := newSearchClient(t, f)
	_, err := c.Search(context.Background(), "huge")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
	if f.deletes != 1 {
		t.Error("search not deleted after an oversized answer")
	}
}

func TestCapResponsesBoundsMemory(t *testing.T) {
	var in []SearchResponse
	for i := 0; i < maxResponses+50; i++ {
		files := make([]SearchFile, 100)
		for j := range files {
			files[j] = SearchFile{Filename: "a.epub", Size: 1}
		}
		in = append(in, SearchResponse{Username: "u" + strconv.Itoa(i), Files: files})
	}
	in = append(in, SearchResponse{Username: "", Files: []SearchFile{{Filename: "x"}}})
	out := capResponses(in)
	if len(out) > maxResponses {
		t.Errorf("kept %d responses, cap is %d", len(out), maxResponses)
	}
	total := 0
	for _, r := range out {
		total += len(r.Files)
	}
	if total > maxFiles {
		t.Errorf("kept %d files, cap is %d", total, maxFiles)
	}
	if got := capResponses([]SearchResponse{{Username: " ", Files: []SearchFile{{}}}, {Username: "u"}}); len(got) != 0 {
		t.Errorf("responses with no user or no files kept: %v", got)
	}
}
