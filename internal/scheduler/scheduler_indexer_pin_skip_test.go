package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

type recordingNotifier struct {
	mu     sync.Mutex
	events []map[string]interface{}
	types  []string
}

func (n *recordingNotifier) Send(_ context.Context, eventType string, payload map[string]interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.types = append(n.types, eventType)
	n.events = append(n.events, payload)
}

// pinnedFirstFixture has a pinned indexer ranked first whose client is down
// (disabled), and an unpinned indexer ranked second with its own release.
func pinnedFirstFixture(t *testing.T) (*schedPinFixture, *models.Indexer) {
	t.Helper()
	f := newSchedPinFixture(t)
	f.pin(t, f.privC.ID)
	f.privC.Enabled = false
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	open := &models.Indexer{Name: "OpenIdx", Type: "newznab", URL: "http://open.invalid", Enabled: true}
	if err := f.indexers.Create(f.ctx, open); err != nil {
		t.Fatal(err)
	}
	pinnedRes := f.s.searcher.(*perFormatSearcher).byFormat[models.MediaTypeEbook][0]
	pinnedRes2 := pinnedRes
	pinnedRes2.GUID = "g-pin-2"
	pinnedRes2.Title = "Pin Author Pinned Book epub v2"
	openRes := pinnedRes
	openRes.GUID = "g-open"
	openRes.Title = "Pin Author Pinned Book epub open"
	openRes.IndexerID = open.ID
	openRes.IndexerName = "OpenIdx"
	f.s.searcher.(*perFormatSearcher).byFormat[models.MediaTypeEbook] = []newznab.SearchResult{pinnedRes, pinnedRes2, openRes}
	return f, open
}

func (f *schedPinFixture) pinnedFailures(t *testing.T) []models.HistoryEvent {
	t.Helper()
	events, err := f.history.ListByBook(f.ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []models.HistoryEvent
	for _, e := range events {
		if e.EventType == models.HistoryEventDownloadFailed && strings.Contains(e.Data, "indexer_client_unavailable") {
			out = append(out, e)
		}
	}
	return out
}

// TestAutoGrab_FailingPinnedReleaseDoesNotStarveUnpinnedIndexers: a pinned
// release whose client is down is skipped, the next approved release from a
// different indexer is grabbed, and nothing for the pinned indexer reaches
// another client.
func TestAutoGrab_FailingPinnedReleaseDoesNotStarveUnpinnedIndexers(t *testing.T) {
	f, _ := pinnedFirstFixture(t)
	f.s.SearchAndGrabBook(f.ctx, f.book)

	if f.priv.count() != 0 {
		t.Fatalf("the disabled pinned client received a grab")
	}
	if f.pub.count() != 1 {
		t.Fatalf("public client grabs = %d, want exactly the unpinned indexer's release", f.pub.count())
	}
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-open"); dl == nil {
		t.Errorf("the unpinned indexer's release was not grabbed")
	}
	for _, guid := range []string{"g-pin", "g-pin-2"} {
		if dl, _ := f.downloads.GetByGUID(f.ctx, guid); dl != nil {
			t.Errorf("the pinned indexer's release %s was sent somewhere: %+v", guid, dl)
		}
	}
	// Recorded once for the book and indexer, though two of its releases were skipped.
	if n := len(f.pinnedFailures(t)); n != 1 {
		t.Errorf("pinned failure events = %d, want 1", n)
	}
}

// TestAutoGrab_PinnedFailureIsRecordedOncePerDay: the next sweep skips the
// same release again, and neither History nor the notifier hears about it twice.
func TestAutoGrab_PinnedFailureIsRecordedOncePerDay(t *testing.T) {
	f, _ := pinnedFirstFixture(t)
	n := &recordingNotifier{}
	f.s.notif = n
	f.s.SearchAndGrabBook(f.ctx, f.book)
	// Free the second sweep from the live row of the first sweep's grab.
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-open"); dl != nil {
		_ = f.downloads.Delete(f.ctx, dl.ID)
	}
	f.s.SearchAndGrabBook(f.ctx, f.book)

	if got := len(f.pinnedFailures(t)); got != 1 {
		t.Fatalf("pinned failure events after two sweeps = %d, want 1", got)
	}
	failed := 0
	for i, typ := range n.types {
		if typ != "downloadFailed" {
			continue
		}
		failed++
		body, _ := json.Marshal(n.events[i])
		if !strings.Contains(string(body), "TrackerX") {
			t.Errorf("notification does not name the indexer: %s", body)
		}
		if strings.Contains(string(body), "sab-private") {
			t.Errorf("notification names the client: %s", body)
		}
	}
	if failed != 1 {
		t.Errorf("downloadFailed notifications = %d, want 1", failed)
	}
}

// TestAutoGrab_PinnedFailureIsRecordedAgainAfterADay: a failure older than 24
// hours no longer suppresses a new one.
func TestAutoGrab_PinnedFailureIsRecordedAgainAfterADay(t *testing.T) {
	f, _ := pinnedFirstFixture(t)
	f.s.SearchAndGrabBook(f.ctx, f.book)
	old := time.Now().UTC().Add(-25 * time.Hour)
	if _, err := f.database.ExecContext(f.ctx, `UPDATE history SET created_at=? WHERE event_type='downloadFailed'`, old); err != nil {
		t.Fatal(err)
	}
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-open"); dl != nil {
		_ = f.downloads.Delete(f.ctx, dl.ID)
	}
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if got := len(f.pinnedFailures(t)); got != 2 {
		t.Fatalf("pinned failure events = %d, want 2", got)
	}
}
