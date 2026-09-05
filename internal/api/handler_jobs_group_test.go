package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/models"
)

// blockingImportScanner parks ImportFromPath until release is closed, so a test
// can observe whether shutdown WAITS for the import or walks straight past it.
type blockingImportScanner struct {
	stubManualImportScanner
	started chan struct{}
	release chan struct{}
}

func (s *blockingImportScanner) ImportFromPath(ctx context.Context, dl *models.Download, path, hint string) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	s.stubManualImportScanner.ImportFromPath(ctx, dl, path, hint)
}

// batchImportRequest seeds a book and a real file, and returns the request body
// for a one-item batch import of it.
func batchImportRequest(t *testing.T, authors *db.AuthorRepo, books *db.BookRepo, ctx context.Context) []byte {
	t.Helper()
	book := seedBook(t, authors, books, ctx)
	path := filepath.Join(t.TempDir(), "good.epub")
	writeTestFile(t, path)
	body, err := json.Marshal([]map[string]any{
		{"path": path, "bookId": book.ID, "format": models.MediaTypeEbook},
	})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	return body
}

// TestManualImportBatch_ShutdownDrainsTheImport is the regression guard for
// #2371 on the manual-import side. The batch fan-out used to be a bare
// goroutine, so a SIGTERM mid-import closed the database under it. Routed
// through the jobs group, Shutdown must block until it finishes.
func TestManualImportBatch_ShutdownDrainsTheImport(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	scanner := &blockingImportScanner{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	group := jobs.NewGroup(context.Background())
	h := NewManualImportHandler(scanner, db.NewDownloadRepo(database), books).WithJobs(group)

	rec := httptest.NewRecorder()
	h.ImportBatch(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/batch",
		bytes.NewReader(batchImportRequest(t, authors, books, ctx))))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	select {
	case <-scanner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch import never started")
	}

	drained := make(chan []string, 1)
	go func() { drained <- group.Shutdown(10 * time.Second) }()

	select {
	case names := <-drained:
		t.Fatalf("Shutdown returned while the import was still running (stragglers: %v); the job is not tracked", names)
	case <-time.After(150 * time.Millisecond):
		// Still blocked, which is the point.
	}

	close(scanner.release)
	select {
	case names := <-drained:
		if len(names) != 0 {
			t.Errorf("Shutdown reported jobs still running after release: %v", names)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown never returned after the import was released")
	}
}

// TestManualImportBatch_ShutDownGroupIsRefused pins the other half of the Go
// contract (#2372): once the group is draining, the handler must say so rather
// than answer 202 for work that will never run.
func TestManualImportBatch_ShutDownGroupIsRefused(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	scanner := &stubManualImportScanner{}
	group := jobs.NewGroup(context.Background())
	group.Shutdown(time.Second)
	h := NewManualImportHandler(scanner, db.NewDownloadRepo(database), books).WithJobs(group)

	rec := httptest.NewRecorder()
	h.ImportBatch(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/batch",
		bytes.NewReader(batchImportRequest(t, authors, books, ctx))))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while shutting down; body = %s", rec.Code, rec.Body.String())
	}
	scanner.importMu.Lock()
	calls := scanner.importCalls
	scanner.importMu.Unlock()
	if calls != 0 {
		t.Errorf("ImportFromPath ran %d times for a refused batch", calls)
	}
}

// TestManualImportReassign_ShutDownGroupIsRefused pins the same answer for the
// reassign endpoint (#2372). Reassign has already detached the file from the
// source book by the time it launches the move, so answering 202 for a move
// that never happens would leave the file attached to nothing.
func TestManualImportReassign_ShutDownGroupIsRefused(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	src := seedBook(t, authors, books, ctx)
	target := &models.Book{
		ForeignID: "mi-book-shutdown", AuthorID: src.AuthorID,
		Title: "Correct Book", SortTitle: "correct book",
		Status: "wanted", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, target); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	epub := filepath.Join(t.TempDir(), "mismatched.epub")
	writeTestFile(t, epub)
	if err := books.AddBookFile(ctx, src.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatalf("attach file to source: %v", err)
	}

	scanner := &stubManualImportScanner{}
	group := jobs.NewGroup(context.Background())
	group.Shutdown(time.Second)
	h := NewManualImportHandler(scanner, db.NewDownloadRepo(database), books).WithJobs(group)

	body, err := json.Marshal(map[string]any{
		"path": epub, "targetBookId": target.ID, "format": models.MediaTypeEbook,
	})
	if err != nil {
		t.Fatalf("marshal reassign: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while shutting down; body = %s", rec.Code, rec.Body.String())
	}
	scanner.importMu.Lock()
	calls := scanner.importCalls
	scanner.importMu.Unlock()
	if calls != 0 {
		t.Errorf("ImportFromPath ran %d times for a refused reassign", calls)
	}
}

// TestFetchAuthorBooksAsync_RoutesThroughTheJobsGroup covers the author side of
// #2371. A calibre-prefixed author on a single-work run returns immediately
// inside fetchAuthorBooks, which keeps the assertion about the launch rather
// than about the sync's own work.
func TestFetchAuthorBooksAsync_RoutesThroughTheJobsGroup(t *testing.T) {
	author := &models.Author{ID: 1, Name: "Calibre Author", ForeignID: "calibre:author:1"}
	opts := catalogueSyncOptions{onlyForeignID: "OL1W"}

	t.Run("live group runs and drains it", func(t *testing.T) {
		logs := captureSlog(t)
		group := jobs.NewGroup(context.Background())
		h := &AuthorHandler{}
		h.WithJobs(group)

		h.fetchAuthorBooksAsync(author, opts)
		if names := group.Shutdown(10 * time.Second); len(names) != 0 {
			t.Fatalf("Shutdown reported jobs still running: %v", names)
		}
		// Shutdown drained the job, so the sync's first log line is on record
		// by the time Shutdown returned. A bare goroutine would race this.
		if got := logs.String(); !strings.Contains(got, "fetching books for author") {
			t.Errorf("catalogue sync did not run through the group; logs: %s", got)
		}
	})

	t.Run("shut-down group drops it with a reason", func(t *testing.T) {
		logs := captureSlog(t)
		group := jobs.NewGroup(context.Background())
		group.Shutdown(time.Second)
		h := &AuthorHandler{}
		h.WithJobs(group)

		h.fetchAuthorBooksAsync(author, opts)
		got := logs.String()
		if strings.Contains(got, "fetching books for author") {
			t.Errorf("catalogue sync ran on a shut-down group; logs: %s", got)
		}
		if !strings.Contains(got, "server is shutting down") {
			t.Errorf("dropped catalogue sync left no trace; logs: %s", got)
		}
	})
}
