package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
)

func postIntegrity(ctx context.Context, h *DBIntegrityHandler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.Check(rec, httptest.NewRequest(http.MethodPost, "/system/db-integrity", nil).WithContext(ctx))
	return rec
}

func emptyScan(context.Context) ([]db.ForeignKeyDrift, error) { return nil, nil }

func TestDBIntegrityCheck_Shape(t *testing.T) {
	h := &DBIntegrityHandler{
		timeout: time.Second,
		now:     func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) },
		scan: func(context.Context) ([]db.ForeignKeyDrift, error) {
			return []db.ForeignKeyDrift{
				{ChildTable: "book_files", ParentTable: "books", OnDelete: "CASCADE", Violations: 4},
				{ChildTable: "downloads", ParentTable: "books", OnDelete: "SET NULL", Violations: 2},
			}, nil
		},
	}
	rec := postIntegrity(context.Background(), h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got dbIntegrityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 6 || len(got.Relationships) != 2 {
		t.Fatalf("got %+v", got)
	}
	if r := got.Relationships[0]; r.ChildTable != "book_files" || r.ParentTable != "books" || r.OnDelete != "CASCADE" || r.Violations != 4 {
		t.Errorf("first relationship = %+v", r)
	}
	if !got.CheckedAt.Equal(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("checkedAt = %v", got.CheckedAt)
	}
	// Counts only: the wire keys are exactly these.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for k := range raw {
		if k != "relationships" && k != "total" && k != "checkedAt" {
			t.Errorf("unexpected top level key %q", k)
		}
	}
	for _, rel := range raw["relationships"].([]any) {
		for k := range rel.(map[string]any) {
			switch k {
			case "childTable", "parentTable", "onDelete", "violations":
			default:
				t.Errorf("unexpected relationship key %q", k)
			}
		}
	}
}

func TestDBIntegrityCheck_CleanIsEmptyArray(t *testing.T) {
	h := NewDBIntegrityHandler(nil, "", nil)
	h.scan = emptyScan
	rec := postIntegrity(context.Background(), h)
	if !strings.Contains(rec.Body.String(), `"relationships":[]`) || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("clean body = %s", rec.Body.String())
	}
}

func TestDBIntegrityCheck_RealDatabase(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO book_files (id, book_id, format, path) VALUES (1, 99, 'ebook', '/l/1.epub')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	// ":memory:" and "file:" paths cannot be reopened, so they must use the
	// shared connection rather than scanning a fresh empty database.
	for _, path := range []string{"", ":memory:", "file:x.db"} {
		rec := postIntegrity(context.Background(), NewDBIntegrityHandler(database, path, nil))
		var got dbIntegrityResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 1 || len(got.Relationships) != 1 || got.Relationships[0].ChildTable != "book_files" {
			t.Fatalf("path %q: got %+v", path, got)
		}
	}
}

// A second click while a scan runs is refused, and the first scan still
// completes and releases the lock.
func TestDBIntegrityCheck_SingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	h := &DBIntegrityHandler{
		timeout: time.Minute,
		now:     time.Now,
		scan: func(context.Context) ([]db.ForeignKeyDrift, error) {
			calls++
			close(started)
			<-release
			return nil, nil
		},
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- postIntegrity(context.Background(), h) }()
	<-started

	second := postIntegrity(context.Background(), h)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "already running") {
		t.Fatalf("second request = %d %s, want 409 already running", second.Code, second.Body.String())
	}
	close(release)
	if first := <-done; first.Code != http.StatusOK {
		t.Fatalf("first request = %d", first.Code)
	}
	if calls != 1 {
		t.Errorf("scan ran %d times, want 1", calls)
	}
	h.scan = emptyScan
	if rec := postIntegrity(context.Background(), h); rec.Code != http.StatusOK {
		t.Errorf("lock not released: %d", rec.Code)
	}
}

func TestDBIntegrityCheck_TimeoutReleasesLock(t *testing.T) {
	h := &DBIntegrityHandler{
		timeout: 20 * time.Millisecond,
		now:     time.Now,
		scan: func(ctx context.Context) ([]db.ForeignKeyDrift, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	rec := postIntegrity(context.Background(), h)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "took too long") {
		t.Fatalf("timeout = %d %s", rec.Code, rec.Body.String())
	}
	h.scan = emptyScan
	if rec := postIntegrity(context.Background(), h); rec.Code != http.StatusOK {
		t.Errorf("lock held after timeout: %d", rec.Code)
	}
}

func TestDBIntegrityCheck_ClientCancelReleasesLock(t *testing.T) {
	h := &DBIntegrityHandler{
		timeout: time.Minute,
		now:     time.Now,
		scan: func(ctx context.Context) ([]db.ForeignKeyDrift, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := postIntegrity(ctx, h)
	if rec.Code == http.StatusGatewayTimeout || strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("cancelled request answered %d %s", rec.Code, rec.Body.String())
	}
	h.scan = emptyScan
	if rec := postIntegrity(context.Background(), h); rec.Code != http.StatusOK {
		t.Errorf("lock held after cancel: %d", rec.Code)
	}
}

func TestDBIntegrityCheck_ScanErrorIs500WithoutDetail(t *testing.T) {
	h := &DBIntegrityHandler{
		timeout: time.Second,
		now:     time.Now,
		scan: func(context.Context) ([]db.ForeignKeyDrift, error) {
			return nil, errors.New("secret /path/db.sqlite broke")
		},
	}
	rec := postIntegrity(context.Background(), h)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}
