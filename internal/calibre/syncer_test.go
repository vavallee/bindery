package calibre

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// waitUntil polls the predicate every millisecond until it returns true or
// the deadline elapses. Used to wait for the Syncer's background goroutine
// to reach/leave a state without busy-spinning without yielding.
func waitUntil(t *testing.T, timeout time.Duration, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// fakeBookLister is a test double for the syncer's BookLister dependency.
// Captures SetCalibreID calls so we can assert that both fresh pushes and
// 409-conflict responses persist the id when one is returned.
type fakeBookLister struct {
	books []models.Book
	mu    sync.Mutex
	set   map[int64]int64 // bookID → calibreID
}

func (f *fakeBookLister) ListByStatus(_ context.Context, _ string) ([]models.Book, error) {
	return f.books, nil
}

func (f *fakeBookLister) SetCalibreID(_ context.Context, id, calibreID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set == nil {
		f.set = map[int64]int64{}
	}
	f.set[id] = calibreID
	return nil
}

// fakePusher is an inline pluginPusher that dispatches per-path behaviour
// so we can verify pushed / already_in_calibre / failed all get counted
// correctly inside one sync run.
type fakePusher struct {
	calls map[string]func() (int64, error)
}

func (f *fakePusher) Add(_ context.Context, path string) (int64, error) {
	fn, ok := f.calls[path]
	if !ok {
		return 0, errors.New("unexpected path")
	}
	return fn()
}

func TestSyncer_Start_MixedOutcomesCountedCorrectly(t *testing.T) {
	books := &fakeBookLister{
		books: []models.Book{
			{ID: 1, Title: "Fresh", FilePath: "/l/fresh.epub"},
			{ID: 2, Title: "Duplicate", FilePath: "/l/dup.epub"},
			{ID: 3, Title: "Broken", FilePath: "/l/broken.epub"},
			{ID: 4, Title: "NoFile"}, // must be filtered out by the syncer
		},
	}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/l/fresh.epub":   func() (int64, error) { return 101, nil },
		"/l/dup.epub":     func() (int64, error) { return 202, ErrAlreadyInCalibre },
		"/l/broken.epub":  func() (int64, error) { return 0, errors.New("boom") },
	}}

	s := NewSyncer(books)
	// Inject the fake pusher in place of the real HTTP client factory.
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })
	p := s.Progress()
	if p.Stats.Total != 3 { // book 4 has no file path; excluded
		t.Errorf("Total: got %d, want 3", p.Stats.Total)
	}
	if p.Stats.Pushed != 1 {
		t.Errorf("Pushed: got %d, want 1", p.Stats.Pushed)
	}
	if p.Stats.AlreadyInCalibre != 1 {
		t.Errorf("AlreadyInCalibre: got %d, want 1", p.Stats.AlreadyInCalibre)
	}
	if p.Stats.Failed != 1 {
		t.Errorf("Failed: got %d, want 1", p.Stats.Failed)
	}
	if len(p.Errors) != 1 || p.Errors[0].BookID != 3 {
		t.Errorf("Errors: got %+v, want one entry for book 3", p.Errors)
	}
	if books.set[1] != 101 {
		t.Errorf("expected fresh book to persist calibre_id=101, got %d", books.set[1])
	}
	if books.set[2] != 202 {
		t.Errorf("expected duplicate book to persist calibre_id=202 from 409 response, got %d", books.set[2])
	}
}

func TestSyncer_Start_RejectsNonPluginMode(t *testing.T) {
	s := NewSyncer(&fakeBookLister{})
	if err := s.Start(context.Background(), Config{}, ModeCalibredb); !errors.Is(err, ErrSyncModeNotPlugin) {
		t.Errorf("want ErrSyncModeNotPlugin, got %v", err)
	}
}

func TestSyncer_Start_RejectsConcurrentRuns(t *testing.T) {
	// Feed the first run one book whose push blocks, so the second Start
	// observes running=true. Use a channel instead of a sleep to keep the
	// test deterministic.
	release := make(chan struct{})
	books := &fakeBookLister{books: []models.Book{{ID: 1, Title: "A", FilePath: "/x"}}}
	pusher := &fakePusher{calls: map[string]func() (int64, error){
		"/x": func() (int64, error) { <-release; return 1, nil },
	}}
	s := NewSyncer(books)
	s.newClient = func(_ Config) pluginPusher { return pusher }

	if err := s.Start(context.Background(), Config{}, ModePlugin); err != nil {
		t.Fatalf("first start: %v", err)
	}
	waitUntil(t, time.Second, s.Running)
	if err := s.Start(context.Background(), Config{}, ModePlugin); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Errorf("second start: want ErrSyncAlreadyRunning, got %v", err)
	}
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return !s.Running() })
}
