package indexer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

type recordedFailure struct {
	id      int64
	code    int
	message string
}

// fakeHealthStore records what the searcher asked to persist.
type fakeHealthStore struct {
	mu        sync.Mutex
	failures  []recordedFailure
	successes []int64
}

func (f *fakeHealthStore) RecordSearchFailure(_ context.Context, id int64, code int, message string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, recordedFailure{id, code, message})
	return nil
}

func (f *fakeHealthStore) RecordSearchSuccess(_ context.Context, id int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, id)
	return nil
}

func (f *fakeHealthStore) snapshot() ([]recordedFailure, []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedFailure(nil), f.failures...), append([]int64(nil), f.successes...)
}

type sentEvent struct {
	event   string
	payload map[string]interface{}
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []sentEvent
}

func (f *fakeNotifier) Send(_ context.Context, event string, payload map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentEvent{event, payload})
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newTestHealth(store IndexerHealthStore, notif healthEventNotifier, now func() time.Time) *indexerHealth {
	return &indexerHealth{store: store, notif: notif, last: make(map[int64]healthSnapshot), now: now}
}

func authError(code int, desc string) error {
	return &newznab.IndexerError{Code: code, Description: desc}
}

var testIndexer = models.Indexer{ID: 7, Name: "Suspended Tracker"}

// TestIndexerHealth_RecordsFailureWithCode is the reported symptom: an indexer
// answering "Account suspended" leaves a trace on the row instead of vanishing
// into a log line.
func TestIndexerHealth_RecordsFailureWithCode(t *testing.T) {
	store := &fakeHealthStore{}
	h := newTestHealth(store, nil, nil)

	h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))

	failures, _ := store.snapshot()
	if len(failures) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(failures))
	}
	if failures[0].id != testIndexer.ID || failures[0].code != 101 {
		t.Errorf("recorded %+v, want id %d code 101", failures[0], testIndexer.ID)
	}
}

// TestIndexerHealth_TransportFailureHasNoCode: a connection error is not the
// indexer rejecting us, so it must not be stored as a Newznab code and must not
// be treated as needing a human.
func TestIndexerHealth_TransportFailureHasNoCode(t *testing.T) {
	store := &fakeHealthStore{}
	notif := &fakeNotifier{}
	h := newTestHealth(store, notif, nil)

	h.recordFailure(context.Background(), testIndexer, errors.New("dial tcp: connection refused"))

	failures, _ := store.snapshot()
	if len(failures) != 1 || failures[0].code != 0 {
		t.Fatalf("recorded %+v, want a single failure with code 0", failures)
	}
	if notif.count() != 0 {
		t.Errorf("a transport failure notified %d times, want 0", notif.count())
	}
}

// TestIndexerHealth_RepeatFailureWritesOnce is the anti-spam guard. A scheduled
// wanted scan searches per book, so writing on every failed search would mean
// hundreds of identical UPDATEs and hundreds of notifications.
func TestIndexerHealth_RepeatFailureWritesOnce(t *testing.T) {
	store := &fakeHealthStore{}
	notif := &fakeNotifier{}
	now := time.Now()
	h := newTestHealth(store, notif, func() time.Time { return now })

	for i := 0; i < 50; i++ {
		h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))
	}

	failures, _ := store.snapshot()
	if len(failures) != 1 {
		t.Errorf("wrote %d times for an unchanged failure, want 1", len(failures))
	}
	if notif.count() != 1 {
		t.Errorf("notified %d times for one transition, want 1", notif.count())
	}
}

// TestIndexerHealth_RefreshesAfterInterval: an unchanged state is still
// refreshed occasionally so the stored timestamp does not go stale forever.
func TestIndexerHealth_RefreshesAfterInterval(t *testing.T) {
	store := &fakeHealthStore{}
	notif := &fakeNotifier{}
	now := time.Now()
	h := newTestHealth(store, notif, func() time.Time { return now })

	h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))
	now = now.Add(healthRefreshInterval + time.Second)
	h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))

	failures, _ := store.snapshot()
	if len(failures) != 2 {
		t.Errorf("wrote %d times across the refresh interval, want 2", len(failures))
	}
	// Still one transition, so still one notification.
	if notif.count() != 1 {
		t.Errorf("notified %d times, want 1", notif.count())
	}
}

// TestIndexerHealth_SuccessClearsAndRenotifies: clearing on success is what
// stops a badge outliving the problem, and a failure after a recovery is a
// fresh transition that should notify again.
func TestIndexerHealth_SuccessClearsAndRenotifies(t *testing.T) {
	store := &fakeHealthStore{}
	notif := &fakeNotifier{}
	now := time.Now()
	h := newTestHealth(store, notif, func() time.Time { return now })

	h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))
	h.recordSuccess(context.Background(), testIndexer)
	h.recordFailure(context.Background(), testIndexer, authError(101, "Account suspended"))

	failures, successes := store.snapshot()
	if len(successes) != 1 {
		t.Errorf("recorded %d successes, want 1", len(successes))
	}
	if len(failures) != 2 {
		t.Errorf("recorded %d failures, want 2", len(failures))
	}
	if notif.count() != 2 {
		t.Errorf("notified %d times across two separate failures, want 2", notif.count())
	}
}

// TestIndexerHealth_RepeatSuccessWritesOnce keeps a healthy indexer from
// generating an UPDATE per search.
func TestIndexerHealth_RepeatSuccessWritesOnce(t *testing.T) {
	store := &fakeHealthStore{}
	now := time.Now()
	h := newTestHealth(store, nil, func() time.Time { return now })

	for i := 0; i < 20; i++ {
		h.recordSuccess(context.Background(), testIndexer)
	}

	_, successes := store.snapshot()
	if len(successes) != 1 {
		t.Errorf("wrote %d times for an unchanged success, want 1", len(successes))
	}
}

// TestIndexerHealth_RateLimitDoesNotNotify: a 5xx clears on its own and #1934
// already stops asking. Telling a user about it would be noise they cannot act
// on, so it is recorded but not notified.
func TestIndexerHealth_RateLimitDoesNotNotify(t *testing.T) {
	store := &fakeHealthStore{}
	notif := &fakeNotifier{}
	h := newTestHealth(store, notif, nil)

	h.recordFailure(context.Background(), testIndexer, authError(500, "Request limit reached"))

	failures, _ := store.snapshot()
	if len(failures) != 1 || failures[0].code != 500 {
		t.Fatalf("recorded %+v, want one failure with code 500", failures)
	}
	if notif.count() != 0 {
		t.Errorf("a rate limit notified %d times, want 0", notif.count())
	}
}

// TestIndexerHealth_NilStoreIsInert: a searcher with no health wiring must
// behave exactly as it did before, which is what keeps every existing caller
// and test unaffected.
func TestIndexerHealth_NilStoreIsInert(t *testing.T) {
	var h *indexerHealth
	h.recordFailure(context.Background(), testIndexer, authError(101, "nope"))
	h.recordSuccess(context.Background(), testIndexer)
}

// TestIndexerHealth_UnsavedIndexerIsSkipped: id 0 has nothing stable to key on.
func TestIndexerHealth_UnsavedIndexerIsSkipped(t *testing.T) {
	store := &fakeHealthStore{}
	h := newTestHealth(store, nil, nil)

	h.recordFailure(context.Background(), models.Indexer{Name: "unsaved"}, authError(101, "nope"))

	failures, _ := store.snapshot()
	if len(failures) != 0 {
		t.Errorf("recorded %d failures for an unsaved indexer, want 0", len(failures))
	}
}

// TestNeedsAttention covers the split the UI and the notifier both branch on.
func TestNeedsAttention(t *testing.T) {
	code := func(c int) *int { return &c }
	msg := "boom"
	cases := []struct {
		name string
		idx  models.Indexer
		want bool
	}{
		{"never searched", models.Indexer{}, false},
		{"bad credentials", models.Indexer{LastError: &msg, LastErrorCode: code(100)}, true},
		{"account suspended", models.Indexer{LastError: &msg, LastErrorCode: code(101)}, true},
		{"rate limited", models.Indexer{LastError: &msg, LastErrorCode: code(500)}, false},
		{"transport failure", models.Indexer{LastError: &msg}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.idx.NeedsAttention(); got != tc.want {
				t.Errorf("NeedsAttention() = %v, want %v", got, tc.want)
			}
		})
	}
}
