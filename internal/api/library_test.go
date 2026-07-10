package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
)

// fakeScanner records the context it received so the test can inspect it,
// and can simulate an in-flight scan via err.
type fakeScanner struct {
	called chan context.Context
	err    error
}

func (f *fakeScanner) StartScan(ctx context.Context) error {
	if f.err != nil {
		return f.err
	}
	f.called <- ctx
	return nil
}

func newLibraryHandler(t *testing.T) *LibraryHandler {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	scanner := importer.NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		db.NewBookRepo(database),
		db.NewAuthorRepo(database),
		db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	)
	return NewLibraryHandler(scanner)
}

// TestLibraryScan_ContextOutlivesRequest is a regression test for issue #55.
// It verifies that cancelling the HTTP request context (which happens the
// instant the 202 response is written) does not cancel the scan goroutine.
func TestLibraryScan_ContextOutlivesRequest(t *testing.T) {
	fake := &fakeScanner{called: make(chan context.Context, 1)}
	h := &LibraryHandler{scanner: fake}

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/library/scan", nil).WithContext(reqCtx)
	h.Scan(httptest.NewRecorder(), req)

	// Simulate the net/http server cancelling the request context after the
	// handler returns (the 202 has been flushed to the client).
	cancel()

	// Receive the context the handler passed to StartScan.
	scanCtx := <-fake.called

	// The scan context must still be live even though the request context was
	// cancelled.
	select {
	case <-scanCtx.Done():
		t.Fatal("scan context was cancelled when the request context was cancelled (issue #55 regression)")
	default:
		// pass — context.WithoutCancel keeps the scan alive
	}
}

// TestLibraryScan_AlreadyRunningReturns409 is the regression test for #1460:
// a scan request while another scan is in flight must surface 409 Conflict
// instead of spawning a second concurrent full walk.
func TestLibraryScan_AlreadyRunningReturns409(t *testing.T) {
	fake := &fakeScanner{err: importer.ErrScanAlreadyRunning}
	h := &LibraryHandler{scanner: fake}

	rec := httptest.NewRecorder()
	h.Scan(rec, httptest.NewRequest(http.MethodPost, "/library/scan", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict while a scan is running, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected non-empty error in response body")
	}
}

func TestLibraryScan_Returns202(t *testing.T) {
	h := newLibraryHandler(t)
	rec := httptest.NewRecorder()
	h.Scan(rec, httptest.NewRequest(http.MethodPost, "/library/scan", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["message"] == "" {
		t.Error("expected non-empty message in response body")
	}
}
