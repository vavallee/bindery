package scheduler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// pendingPinFixture has nothing from search, so the sweep falls through to the
// pending releases, and a pinned indexer whose client is disabled.
func pendingPinFixture(t *testing.T) (*schedPinFixture, *db.PendingReleaseRepo, *models.Indexer) {
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
	f.s.searcher.(*perFormatSearcher).byFormat[models.MediaTypeEbook] = nil
	pending := db.NewPendingReleaseRepo(f.database)
	f.s.pending = pending
	return f, pending, open
}

func (f *schedPinFixture) holdPending(t *testing.T, pending *db.PendingReleaseRepo, guid string, idx *models.Indexer) {
	t.Helper()
	res := newznab.SearchResult{
		GUID: guid, Title: "Pin Author Pinned Book epub " + guid, NZBURL: f.pub.srv.URL + "/" + guid + ".nzb",
		Protocol: "usenet", IndexerID: idx.ID, IndexerName: idx.Name, Size: 2 << 20,
	}
	blob, _ := json.Marshal(res)
	if err := pending.Upsert(f.ctx, &models.PendingRelease{
		BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: res.Title, IndexerID: &idx.ID,
		GUID: guid, Protocol: "usenet", Reason: "delay", ReleaseJSON: string(blob),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPending_ApprovedReleaseOfAnUnavailablePinnedClientStaysPending: the held
// release must not be deleted when the pin refuses it. It stays pending for the
// sweep after the client is back, nothing is grabbed, and the failure is
// recorded once.
func TestPending_ApprovedReleaseOfAnUnavailablePinnedClientStaysPending(t *testing.T) {
	f, pending, _ := pendingPinFixture(t)
	f.holdPending(t, pending, "g-held", f.idx)

	f.s.SearchAndGrabBook(f.ctx, f.book)

	rows, err := pending.ListByBookAndMediaType(f.ctx, f.book.ID, models.MediaTypeEbook)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows after the sweep = %d (err %v), want the held release to survive", len(rows), err)
	}
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("a client received the grab: public=%d private=%d", f.pub.count(), f.priv.count())
	}
	if n := len(f.pinnedFailures(t)); n != 1 {
		t.Errorf("pinned failure events = %d, want 1", n)
	}
	// And once the client is back, the very same release is grabbed from it.
	f.privC.Enabled = true
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	f.s.SearchAndGrabBook(f.ctx, f.book)
	if f.priv.count() != 1 || f.pub.count() != 0 {
		t.Fatalf("after the client came back: public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
}

// TestPending_SkipsAPinnedRowAndTriesTheNextApprovedRow: the first approved
// pending row is pinned and refused, the second is from an unpinned indexer and
// is grabbed in the same sweep.
func TestPending_SkipsAPinnedRowAndTriesTheNextApprovedRow(t *testing.T) {
	f, pending, open := pendingPinFixture(t)
	f.holdPending(t, pending, "g-held-open", open)
	// Newest first: the pinned row is inserted last so it is tried first.
	f.holdPending(t, pending, "g-held-pinned", f.idx)

	f.s.SearchAndGrabBook(f.ctx, f.book)

	if f.priv.count() != 0 || f.pub.count() != 1 {
		t.Fatalf("public=%d private=%d, want the unpinned row grabbed and nothing on the pinned client", f.pub.count(), f.priv.count())
	}
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-held-open"); dl == nil {
		t.Errorf("the unpinned pending release was not grabbed")
	}
	if dl, _ := f.downloads.GetByGUID(f.ctx, "g-held-pinned"); dl != nil {
		t.Errorf("the pinned pending release was sent somewhere: %+v", dl)
	}
	if n := len(f.pinnedFailures(t)); n != 1 {
		t.Errorf("pinned failure events = %d, want 1", n)
	}
}

// TestAutoGrab_PinnedFailureRecordCarriesCodeAndIndexerButNoClient: the History
// event is machine readable for the dedupe, and neither it nor the notification
// names the client or its id.
func TestAutoGrab_PinnedFailureRecordCarriesCodeAndIndexerButNoClient(t *testing.T) {
	f, _ := pinnedFirstFixture(t)
	n := &recordingNotifier{}
	f.s.notif = n
	f.s.SearchAndGrabBook(f.ctx, f.book)

	events := f.pinnedFailures(t)
	if len(events) != 1 {
		t.Fatalf("pinned failure events = %d, want 1", len(events))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(events[0].Data), &data); err != nil {
		t.Fatal(err)
	}
	if data["code"] != "indexer_client_unavailable" {
		t.Errorf("code = %v", data["code"])
	}
	if got, _ := data["indexerId"].(float64); int64(got) != f.idx.ID {
		t.Errorf("indexerId = %v, want %d", data["indexerId"], f.idx.ID)
	}
	if !strings.Contains(events[0].Data, "TrackerX") {
		t.Errorf("history message does not name the indexer: %s", events[0].Data)
	}
	if strings.Contains(events[0].Data, "sab-private") || strings.Contains(events[0].Data, "downloadClient") {
		t.Errorf("history leaks the client: %s", events[0].Data)
	}
	var failed []map[string]interface{}
	for i, typ := range n.types {
		if typ == "downloadFailed" {
			failed = append(failed, n.events[i])
		}
	}
	if len(failed) != 1 {
		t.Fatalf("downloadFailed notifications = %d, want 1", len(failed))
	}
	body, _ := json.Marshal(failed[0])
	if !strings.Contains(string(body), "TrackerX") {
		t.Errorf("notification does not name the indexer: %s", body)
	}
	for _, banned := range []string{"sab-private", "clientId", "downloadClient"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("notification contains %q: %s", banned, body)
		}
	}
	// The client's numeric id must not appear as a field value either.
	for k, v := range failed[0] {
		if k == "clientId" || v == f.privC.ID {
			t.Errorf("notification field %s carries the client id: %v", k, v)
		}
	}
}
