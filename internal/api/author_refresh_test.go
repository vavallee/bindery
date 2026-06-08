package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// stubAuthorLister returns a fixed author set, with an optional error.
type stubAuthorLister struct {
	authors []models.Author
	err     error
}

func (s *stubAuthorLister) List(_ context.Context) ([]models.Author, error) {
	return s.authors, s.err
}

func refreshTestFixture(t *testing.T, lister authorLister, refresh func(*models.Author)) (*AuthorRefreshHandler, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSettingsRepo(database)
	return NewAuthorRefreshHandler(lister, refresh).WithSettings(repo), repo
}

func readStatus(t *testing.T, repo *db.SettingsRepo) authorRefreshStatus {
	t.Helper()
	setting, err := repo.Get(context.Background(), SettingAuthorBulkRefresh)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if setting == nil {
		t.Fatal("no status persisted")
	}
	var st authorRefreshStatus
	if err := json.Unmarshal([]byte(setting.Value), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	return st
}

func TestAuthorRefresh_RunCompletes(t *testing.T) {
	var calls int32
	authors := []models.Author{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}}
	h, repo := refreshTestFixture(t, &stubAuthorLister{authors: authors},
		func(_ *models.Author) { atomic.AddInt32(&calls, 1) })

	// Call run directly for determinism (it normally runs in a goroutine).
	h.run(context.Background())

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("refresh called %d times, want 3", got)
	}
	st := readStatus(t, repo)
	if st.Status != "completed" {
		t.Fatalf("status = %q, want completed", st.Status)
	}
	if st.Done != st.Total || st.Total != 3 {
		t.Fatalf("done=%d total=%d, want 3/3", st.Done, st.Total)
	}
	if st.Failed != 0 {
		t.Fatalf("failed=%d, want 0", st.Failed)
	}
	if st.CompletedAt == "" {
		t.Fatal("completed_at empty")
	}
}

func TestAuthorRefresh_PanicTolerated(t *testing.T) {
	authors := []models.Author{{ID: 1, Name: "A"}, {ID: 2, Name: "boom"}, {ID: 3, Name: "C"}}
	var calls int32
	h, repo := refreshTestFixture(t, &stubAuthorLister{authors: authors}, func(a *models.Author) {
		atomic.AddInt32(&calls, 1)
		if a.Name == "boom" {
			panic("provider exploded")
		}
	})

	h.run(context.Background())

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("refresh called %d times, want 3 (run must not abort on panic)", got)
	}
	st := readStatus(t, repo)
	if st.Status != "completed" {
		t.Fatalf("status = %q, want completed", st.Status)
	}
	if st.Done != 3 {
		t.Fatalf("done=%d, want 3", st.Done)
	}
	if st.Failed != 1 {
		t.Fatalf("failed=%d, want 1", st.Failed)
	}
}

func TestAuthorRefresh_RefreshAll202(t *testing.T) {
	authors := []models.Author{{ID: 1, Name: "A"}}
	done := make(chan struct{})
	h, repo := refreshTestFixture(t, &stubAuthorLister{authors: authors},
		func(_ *models.Author) { close(done) })

	w := httptest.NewRecorder()
	h.RefreshAll(w, httptest.NewRequest(http.MethodPost, "/authors/refresh-all", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh never ran")
	}

	// Poll for the completed terminal state.
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := readStatus(t, repo)
		if st.Status == "completed" {
			if st.Done != 1 {
				t.Fatalf("done=%d, want 1", st.Done)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never completed, last = %q", st.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthorRefresh_ConcurrentReturns409(t *testing.T) {
	// Block the first run inside refresh so it stays "running" while we fire
	// the second request.
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h, _ := refreshTestFixture(t, &stubAuthorLister{authors: []models.Author{{ID: 1}}},
		func(_ *models.Author) {
			once.Do(func() { close(started) })
			<-release
		})

	w1 := httptest.NewRecorder()
	h.RefreshAll(w1, httptest.NewRequest(http.MethodPost, "/authors/refresh-all", nil))
	if w1.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", w1.Code)
	}
	<-started // first job is now mid-flight (running == true)

	w2 := httptest.NewRecorder()
	h.RefreshAll(w2, httptest.NewRequest(http.MethodPost, "/authors/refresh-all", nil))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409", w2.Code)
	}
	close(release)
}

func TestAuthorRefresh_StatusIdleBeforeRun(t *testing.T) {
	h, _ := refreshTestFixture(t, &stubAuthorLister{}, func(_ *models.Author) {})
	w := httptest.NewRecorder()
	h.RefreshAllStatus(w, httptest.NewRequest(http.MethodGet, "/authors/refresh-all/status", nil))
	// Before any job has run, status is a 200 idle state (not a 404) so the
	// Authors page poll doesn't surface a 404 on every load.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "idle" {
		t.Fatalf("status field = %q, want %q", got["status"], "idle")
	}
}

func TestAuthorRefresh_StaleRunningReconciledToFailed(t *testing.T) {
	h, repo := refreshTestFixture(t, &stubAuthorLister{}, func(_ *models.Author) {})
	// Simulate a job that was "running" when the process died: persist a
	// running status but leave h.running == false.
	stale := authorRefreshStatus{Status: "running", Total: 5, Done: 2, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(stale)
	if err := repo.Set(context.Background(), SettingAuthorBulkRefresh, string(b)); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.RefreshAllStatus(w, httptest.NewRequest(http.MethodGet, "/authors/refresh-all/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got authorRefreshStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Message != "interrupted by restart" {
		t.Fatalf("message = %q, want interrupted by restart", got.Message)
	}
	// The reconciled value must be written back to the store.
	if persisted := readStatus(t, repo); persisted.Status != "failed" {
		t.Fatalf("persisted status = %q, want failed", persisted.Status)
	}
}

func TestAuthorRefresh_ListErrorWritesFailed(t *testing.T) {
	h, repo := refreshTestFixture(t, &stubAuthorLister{err: context.DeadlineExceeded},
		func(_ *models.Author) {})
	h.run(context.Background())
	st := readStatus(t, repo)
	if st.Status != "failed" {
		t.Fatalf("status = %q, want failed", st.Status)
	}
}

func TestAuthorRefresh_NoRefreshFunc400(t *testing.T) {
	h := NewAuthorRefreshHandler(&stubAuthorLister{}, nil)
	w := httptest.NewRecorder()
	h.RefreshAll(w, httptest.NewRequest(http.MethodPost, "/authors/refresh-all", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
