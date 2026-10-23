package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
)

// grabURL runs grab and writeGrabError the way doGrab does, for a release URL
// of the test's choosing and a caller that may omit the indexer id.
func (f *pinFixture) grabURL(guid, nzbURL string, indexerID *int64, nonAdmin bool) *httptest.ResponseRecorder {
	r := asAdmin(httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", nil))
	if nonAdmin {
		r = r.WithContext(auth.WithUserRole(auth.WithUserID(f.ctx, 7), "user"))
	}
	req := grabRequest{GUID: guid, Title: "Pin Author - Pinned Book epub", NZBURL: nzbURL, Size: 100, BookID: &f.book.ID, Protocol: "usenet", IndexerID: indexerID}
	w := httptest.NewRecorder()
	if _, err := f.h.grab(r.Context(), req); err != nil {
		writeGrabError(w, r, err)
		return w
	}
	w.WriteHeader(http.StatusAccepted)
	return w
}

func (f *pinFixture) addIndexer(t *testing.T, name, path string, pin *int64) *models.Indexer {
	t.Helper()
	idx := &models.Indexer{Name: name, Type: "newznab", URL: f.idx.URL[:strings.LastIndex(f.idx.URL, "/")] + path, APIKey: "secret", Enabled: true, DownloadClientID: pin}
	if err := f.indexers.Create(f.ctx, idx); err != nil {
		t.Fatalf("create indexer %s: %v", name, err)
	}
	return idx
}

func (f *pinFixture) wantReached(t *testing.T, w *httptest.ResponseRecorder, pub, priv int) {
	t.Helper()
	if f.pub.count() != pub || f.priv.count() != priv {
		t.Fatalf("public=%d private=%d, want %d and %d (status %d: %s)", f.pub.count(), f.priv.count(), pub, priv, w.Code, w.Body.String())
	}
}

// (a) A pre signed URL with no indexer id: signing returns early and attributes
// nothing, so the pin used to be lost and the release went to the public client.
func TestGrab_PreSignedURLWithoutIndexerIDStillUsesPin(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	w := f.grabURL("g-presigned", f.relURL+"?apikey=secret", nil, false)
	f.wantReached(t, w, 0, 1)
}

// (b) Two indexers share the download host and key. Signing works but nothing
// is attributed. With one of them pinned the grab cannot be tied to a single
// indexer, so it fails rather than guessing, and says why.
func TestGrab_AmbiguousHostWithAPinnedIndexerFailsWithoutFallback(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.addIndexer(t, "TrackerY", "/api2", nil)

	w := f.grabURL("g-ambiguous", f.relURL, nil, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != codeIndexerClientUnavailable {
		t.Errorf("code = %q, want %q", body["code"], codeIndexerClientUnavailable)
	}
	for _, want := range []string{"could not be tied to one indexer", "TrackerX", "pinned", "was not sent"} {
		if !strings.Contains(body["error"], want) {
			t.Errorf("admin message %q does not mention %q", body["error"], want)
		}
	}
	f.wantReached(t, w, 0, 0)

	// A non admin is told the release is held back, with no indexer or client names.
	w = f.grabURL("g-ambiguous2", f.relURL, nil, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non admin: status %d, want 400: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "TrackerX") || strings.Contains(w.Body.String(), "sab-private") {
		t.Errorf("non admin message leaks names: %s", w.Body.String())
	}
	f.wantReached(t, w, 0, 0)
}

// (c) A pinned indexer with no stored API key cannot sign, so signing
// attributed nothing. Its host still identifies it.
func TestGrab_PinnedIndexerWithoutAPIKeyAndNoIDStillUsesPin(t *testing.T) {
	f := newPinFixture(t)
	f.idx.APIKey = ""
	f.pin(t, &f.privC.ID)
	w := f.grabURL("g-nokey", f.relURL, nil, false)
	f.wantReached(t, w, 0, 1)
}

// An ambiguous host where no matching indexer is pinned is untouched: the grab
// proceeds exactly as before.
func TestGrab_AmbiguousHostWithNoPinnedIndexerProceeds(t *testing.T) {
	f := newPinFixture(t)
	f.addIndexer(t, "TrackerY", "/api2", nil)
	w := f.grabURL("g-ambiguous-unpinned", f.relURL, nil, false)
	f.wantReached(t, w, 1, 0)
}

// A pinned indexer exists, but on a different host from the release: nothing ties
// the release to it. This is the documented limit (case d), the grab is
// unpinned, and the advice is to send indexerId.
func TestGrab_ReleaseOnAnUnrelatedHostIsUnpinned(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	w := f.grabURL("g-elsewhere", otherHostURL(f.pub.srv.URL)+"/other.nzb", nil, false)
	f.wantReached(t, w, 1, 0)
}

// A release from an unpinned indexer on its own host, with no id, is unaffected.
func TestGrab_UnpinnedIndexerOnItsHostWithoutIDProceeds(t *testing.T) {
	f := newPinFixture(t)
	w := f.grabURL("g-plain", f.relURL, nil, false)
	f.wantReached(t, w, 1, 0)
}

// Retry of a row whose indexer was deleted (indexer_id is NULL by FK SET NULL)
// finds a recreated pinned indexer again by the release URL's host.
func TestRetry_RowWithNoIndexerFindsPinnedIndexerByHost(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	dl := &models.Download{
		GUID: "g-orphan", Title: "Pin Author - Pinned Book epub", NZBURL: f.relURL + "?apikey=secret", Size: 100,
		Status: models.StateFailed, Protocol: "usenet", Quality: "epub", BookID: &f.book.ID,
	}
	if err := f.downl.Create(f.ctx, dl); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.h.RetryDownload(w, asAdmin(retryDownloadRequest(t, dl.ID)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	f.wantReached(t, w, 0, 1)
}

// A retry that still cannot be tied to any indexer goes the way it always did.
func TestRetry_RowWithNoIndexerOnUnknownHostIsUnpinned(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	dl := &models.Download{
		GUID: "g-orphan2", Title: "Pin Author - Pinned Book epub", NZBURL: otherHostURL(f.pub.srv.URL) + "/gone.nzb", Size: 100,
		Status: models.StateFailed, Protocol: "usenet", Quality: "epub", BookID: &f.book.ID,
	}
	if err := f.downl.Create(f.ctx, dl); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.h.RetryDownload(w, asAdmin(retryDownloadRequest(t, dl.ID)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	f.wantReached(t, w, 1, 0)
}
