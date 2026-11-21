package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// stubClient serves handler as slskd and returns a client for it.
func stubClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return New(u.Hostname(), port, "test-key", "", false)
}

func TestFailureWordNamesEveryReason(t *testing.T) {
	cases := map[TransferState]string{
		StateCompleted | StateRejected:  "rejected by the peer",
		StateCompleted | StateTimedOut:  "timed out",
		StateCompleted | StateCancelled: "cancelled",
		StateCompleted | StateAborted:   "cancelled",
		StateCompleted | StateErrored:   "failed",
		StateCompleted:                  "did not finish",
	}
	for s, want := range cases {
		if got := s.FailureWord(); got != want {
			t.Errorf("%v: %q, want %q", s, got, want)
		}
	}
}

func TestEnqueueBatchRejectsBadFileCounts(t *testing.T) {
	c := stubClient(t, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	if _, err := c.EnqueueBatch(context.Background(), "id", "u", nil, "bindery/1"); err == nil {
		t.Error("an empty file list was sent")
	}
	if _, err := c.EnqueueBatch(context.Background(), "id", "u", make([]BatchFile, MaxGrabFiles+1), "bindery/1"); err == nil {
		t.Error("too many files were sent")
	}
}

func TestEnqueueBatchErrorAnswers(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusConflict, `{}`, "already has a download"},
		{http.StatusInternalServerError, `boom`, "start slskd download"},
		{http.StatusOK, `{not json`, "start slskd download"},
		{http.StatusOK, `{"failures":[{"filename":"a"}]}`, "1 of the 2"},
	} {
		c := stubClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := c.EnqueueBatch(context.Background(), "id", "u", twoFiles(), "bindery/1")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %d: err = %v, want %q", tc.status, err, tc.want)
		}
	}
}

func TestGetBatchErrorsAndGone(t *testing.T) {
	c := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/gone"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/bad"):
			_, _ = w.Write([]byte(`{nope`))
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	})
	if _, err := c.GetBatch(context.Background(), "gone"); !errors.Is(err, ErrBatchGone) {
		t.Errorf("gone: %v", err)
	}
	if _, err := c.GetBatch(context.Background(), "bad"); err == nil {
		t.Error("malformed JSON was accepted")
	}
	if _, err := c.GetBatch(context.Background(), "x"); err == nil {
		t.Error("a 503 was accepted")
	}
}

// A slskd that does not attach transfers to the batch: the peer's download list
// is filtered by batch id instead.
func TestGetBatchFallsBackToThePeersList(t *testing.T) {
	files := []map[string]any{
		{"id": "a", "batchId": "B1", "username": "peer1", "filename": "x", "state": "InProgress"},
		{"id": "b", "batchId": "other", "username": "peer1", "filename": "y", "state": "InProgress"},
	}
	c := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/transfers/downloads/batches/b1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "B1", "username": "peer1"})
		case "/api/v0/transfers/downloads/peer1":
			_ = json.NewEncoder(w).Encode(map[string]any{"directories": []map[string]any{{"files": files}}})
		case "/api/v0/transfers/downloads/ghost":
			http.NotFound(w, r)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	})
	b, err := c.GetBatch(context.Background(), "b1")
	if err != nil || len(b.Transfers) != 1 || b.Transfers[0].ID != "a" {
		t.Fatalf("batch %+v err %v", b, err)
	}
	if list, err := c.userTransfers(context.Background(), "ghost"); err != nil || list != nil {
		t.Errorf("a peer slskd does not know: %v %v", list, err)
	}
	if _, err := c.userTransfers(context.Background(), "broken"); err == nil {
		t.Error("a 503 was accepted")
	}
}

func TestGetBatchFallbackErrorIsReturned(t *testing.T) {
	c := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/batches/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "B1", "username": "peer1"})
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.GetBatch(context.Background(), "b1"); err == nil {
		t.Error("the list failure was swallowed")
	}
}

func TestRemoveTransferAnswers(t *testing.T) {
	c := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/gone"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/fail"):
			http.Error(w, "no", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	if err := c.RemoveTransfer(context.Background(), "u", "ok"); err != nil {
		t.Errorf("ok: %v", err)
	}
	if err := c.RemoveTransfer(context.Background(), "u", "gone"); err != nil {
		t.Errorf("an already removed transfer is not an error: %v", err)
	}
	if err := c.RemoveTransfer(context.Background(), "u", "fail"); err == nil {
		t.Error("a 500 was swallowed")
	}
}

func TestRemoveBatchReportsTheFirstFailureAfterTryingAll(t *testing.T) {
	var deletes []string
	c := stubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/gone"):
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/down"):
			http.Error(w, "down", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "B", "username": "u", "transfers": []map[string]any{
				{"id": "t1"}, {"id": ""}, {"id": "t2"},
			}})
		default:
			deletes = append(deletes, r.URL.Path)
			if strings.Contains(r.URL.Path, "t1") {
				http.Error(w, "no", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	})
	if err := c.RemoveBatch(context.Background(), "gone"); err != nil {
		t.Errorf("a batch slskd lacks is already removed: %v", err)
	}
	if err := c.RemoveBatch(context.Background(), "down"); err == nil {
		t.Error("a batch read failure was swallowed")
	}
	err := c.RemoveBatch(context.Background(), "B")
	if err == nil || !strings.Contains(err.Error(), `"t1"`) {
		t.Fatalf("err = %v", err)
	}
	if len(deletes) != 2 {
		t.Errorf("deletes = %v, want t1 and t2 only (the empty id is skipped)", deletes)
	}
}

func TestListBatchesFillsUsernameAndFailsOnDown(t *testing.T) {
	c := stubClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"username": "peer1", "directories": []map[string]any{{"files": []map[string]any{
			{"id": "1", "batchId": "AbC", "state": "InProgress"},
			{"id": "2", "batchId": "", "state": "InProgress"},
			{"id": "3", "batchId": "abc", "username": "peer2", "state": "InProgress"},
		}}}}})
	})
	got, err := c.ListBatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got["abc"]) != 2 || got["abc"][0].Username != "peer1" || got["abc"][1].Username != "peer2" {
		t.Fatalf("batches = %+v", got)
	}
	bad := stubClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
	if _, err := bad.ListBatches(context.Background()); err == nil {
		t.Error("a 503 was accepted")
	}
}

func TestGrabEncodeAndExtEdges(t *testing.T) {
	if _, err := EncodeGrab(Grab{}); err == nil {
		t.Error("an invalid grab was encoded")
	}
	if got := (Grab{}).Ext(); got != "" {
		t.Errorf("ext of no files = %q", got)
	}
	if got := (Grab{Files: []BatchFile{{Filename: `@@u\A\Book.EPUB`}}}).Ext(); got != "epub" {
		t.Errorf("ext = %q", got)
	}
}
