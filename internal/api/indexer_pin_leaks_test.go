package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func (f *pinFixture) wantPinRefusal(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusBadRequest || !strings.Contains(body, codeIndexerClientUnavailable) {
		t.Fatalf("status %d, want 400 %s: %s", status, codeIndexerClientUnavailable, body)
	}
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("a refused grab reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
}

// Leak 1: the caller names the pinned indexer Z, which has no stored key, while
// a keyed unpinned indexer Y shares the URL host. Signing used to re-attribute
// the grab to Y and Z's own pin was ignored.
func TestGrab_SuppliedPinnedIDWinsOverASigningAttributionToAnotherIndexer(t *testing.T) {
	f := newPinFixture(t)
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	f.addIndexer(t, "TrackerY", "/apiy", nil)
	w := f.grabURL("g-leak1", f.relURL, &f.idx.ID, false)
	f.wantReached(t, w, 0, 1)
}

// Leak 1, second form: the pinned indexer is on another host altogether and the
// URL's host has a keyed unpinned indexer.
func TestGrab_SuppliedPinnedIDOnAnotherHostStillAppliesItsPin(t *testing.T) {
	f := newPinFixture(t)
	f.idx.URL = otherHostURL(f.pub.srv.URL) + "/api"
	f.idx.APIKey = "k"
	f.pin(t, &f.privC.ID)
	// Y is on the release's host, keyed and unpinned.
	y := &models.Indexer{Name: "TrackerY", Type: "newznab", URL: strings.TrimSuffix(f.relURL, "/rel.nzb") + "/apiy", APIKey: "secret", Enabled: true}
	if err := f.indexers.Create(f.ctx, y); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-leak1b", f.relURL, &f.idx.ID, false)
	f.wantReached(t, w, 0, 1)
}

// Leak 2: keyless pinned Z and keyed unpinned Y share the URL host, and the
// caller names an unpinned X on another host. Y signs, which used to excuse the
// refusal.
func TestGrab_ContradictingIDIsRefusedEvenWhenAnotherIndexerCanSignTheURL(t *testing.T) {
	f := newPinFixture(t)
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	f.addIndexer(t, "TrackerY", "/apiy", nil)
	x := &models.Indexer{Name: "TrackerX2", Type: "newznab", URL: otherHostURL(f.pub.srv.URL) + "/api", APIKey: "k", Enabled: true}
	if err := f.indexers.Create(f.ctx, x); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-leak2", f.relURL, &x.ID, false)
	f.wantPinRefusal(t, w.Code, w.Body.String())
}

// Leak 3: an id that matches no indexer is no id at all. With one pinned indexer
// on the URL's host it is that indexer's grab; with a pinned one among several it
// is refused.
func TestGrab_UnknownSuppliedIDIsTreatedAsNoID(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	missing := int64(98765)
	w := f.grabURL("g-leak3a", f.relURL, &missing, false)
	f.wantReached(t, w, 0, 1)

	f2 := newPinFixture(t)
	f2.pin(t, &f2.privC.ID)
	f2.addIndexer(t, "TrackerY", "/apiy", nil)
	w = f2.grabURL("g-leak3b", f2.relURL, &missing, false)
	f2.wantPinRefusal(t, w.Code, w.Body.String())
}

// Leak 4, the opposite direction: a keyed pinned indexer and a keyless unpinned
// one share a host. An id-less grab cannot be told apart, and used to go to the
// pinned indexer's client on signing's pick.
func TestGrab_AmbiguousHostIsRefusedNotRoutedBySigningsPick(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID) // TrackerX: keyed, pinned
	u := f.addIndexer(t, "TrackerU", "/apiu", nil)
	u.APIKey = ""
	if err := f.indexers.Update(f.ctx, u); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-leak4", f.relURL, nil, false)
	f.wantPinRefusal(t, w.Code, w.Body.String())
}

// The row records the decided indexer, not signing's pick.
func TestGrab_DecidedIndexerIsRecordedOnTheRow(t *testing.T) {
	f := newPinFixture(t)
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	f.addIndexer(t, "TrackerY", "/apiy", nil)
	w := f.grabURL("g-row", f.relURL, &f.idx.ID, false)
	f.wantReached(t, w, 0, 1)
	d := f.downloadFor(t, "g-row")
	if d.IndexerID == nil || *d.IndexerID != f.idx.ID {
		t.Errorf("row indexer = %v, want the pinned indexer %d", d.IndexerID, f.idx.ID)
	}
}

// Regression guard: with no pinned indexer anywhere, a supplied unpinned id
// whose host does not match the URL still goes the default way.
func TestGrab_NoPinnedIndexersAnywhereIsUnchanged(t *testing.T) {
	f := newPinFixture(t)
	x := &models.Indexer{Name: "TrackerX2", Type: "newznab", URL: otherHostURL(f.pub.srv.URL) + "/api", APIKey: "k", Enabled: true}
	if err := f.indexers.Create(f.ctx, x); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-nopins", f.relURL, &x.ID, false)
	f.wantReached(t, w, 1, 0)
}
