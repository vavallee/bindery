package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// otherHostURL rewrites a test server's URL onto the host name localhost. The
// pin host match ignores ports on purpose, so two test servers on 127.0.0.1 are
// one host to it, and a genuinely different host is needed for the cases that
// must not match.
func otherHostURL(raw string) string {
	return strings.Replace(raw, "127.0.0.1", "localhost", 1)
}

// A keyless pinned indexer and a keyed unpinned one share the host. Signing can
// only attribute the keyed one, so an id-less grab used to be taken for the
// unpinned indexer and went to the public client. The host check now runs
// whenever the caller named no indexer, and refuses.
func TestGrab_KeylessPinnedAndKeyedUnpinnedOnOneHostIsRefusedWithoutAnID(t *testing.T) {
	f := newPinFixture(t)
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	f.addIndexer(t, "TrackerQ", "/apiq", nil)

	w := f.grabURL("g-dual", f.relURL, nil, false)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeIndexerClientUnavailable) {
		t.Fatalf("status %d, want 400 %s: %s", w.Code, codeIndexerClientUnavailable, w.Body.String())
	}
	f.wantReached(t, w, 0, 0)
}

// The same pair with the caller naming the unpinned indexer truthfully is taken
// at face value: its host matches the release, so it goes the default way.
func TestGrab_TruthfulIDOfAnUnpinnedIndexerSharingAHostWithAPinnedOneGoesPublic(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	q := f.addIndexer(t, "TrackerQ", "/apiq", nil)

	w := f.grabURL("g-truthful", f.relURL, &q.ID, false)
	f.wantReached(t, w, 1, 0)
}

// A supplied id that contradicts the release URL's host, while an indexer that
// does match the host is pinned, is refused. The URL is pre signed so nothing
// else attributes it.
func TestGrab_SuppliedIDContradictingTheURLHostForAPinnedHostIsRefused(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	elsewhere := &models.Indexer{Name: "Elsewhere", Type: "newznab", URL: otherHostURL(f.pub.srv.URL) + "/api", APIKey: "k", Enabled: true}
	if err := f.indexers.Create(f.ctx, elsewhere); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-contra", f.relURL+"?apikey=secret", &elsewhere.ID, false)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeIndexerClientUnavailable) {
		t.Fatalf("status %d, want 400 %s: %s", w.Code, codeIndexerClientUnavailable, w.Body.String())
	}
	f.wantReached(t, w, 0, 0)
}

// A contradicting id with no pinned indexer on the URL's host is not a pin
// problem and proceeds.
func TestGrab_SuppliedIDContradictingTheURLHostWithNoPinnedHostProceeds(t *testing.T) {
	f := newPinFixture(t)
	elsewhere := &models.Indexer{Name: "Elsewhere", Type: "newznab", URL: otherHostURL(f.pub.srv.URL) + "/api", APIKey: "k", Enabled: true}
	if err := f.indexers.Create(f.ctx, elsewhere); err != nil {
		t.Fatal(err)
	}
	w := f.grabURL("g-contra-open", f.relURL+"?apikey=secret", &elsewhere.ID, false)
	f.wantReached(t, w, 1, 0)
}

// A supplied pinned indexer always applies its own pin.
func TestGrab_SuppliedPinnedIDStillAppliesItsPin(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	w := f.grabURL("g-supplied-pinned", f.relURL+"?apikey=secret", &f.idx.ID, false)
	f.wantReached(t, w, 0, 1)
}

// An indexer recorded on another port of the same host (the Prowlarr layout,
// where the application URL and the indexer URL differ by port) is still found
// by an id-less grab.
func TestGrab_IDLessGrabFindsAPinnedIndexerThatDiffersOnlyByPort(t *testing.T) {
	f := newPinFixture(t)
	other := strings.Replace(f.idx.URL, ":"+portOf(t, f.idx.URL), ":"+strconv.Itoa(1+atoi(t, portOf(t, f.idx.URL))), 1)
	f.idx.URL = other
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	w := f.grabURL("g-port", f.relURL, nil, false)
	f.wantReached(t, w, 0, 1)
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	i := strings.LastIndex(raw, ":")
	j := strings.Index(raw[i:], "/")
	if i < 0 || j < 0 {
		t.Fatalf("no port in %q", raw)
	}
	return raw[i+1 : i+j]
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
