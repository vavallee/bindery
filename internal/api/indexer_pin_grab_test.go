package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// pinSAB is a fake SABnzbd that also serves any *.nzb path, and counts the
// addfile calls it receives. Two of them stand in for a public and a private
// client, so a test can say which one a grab reached.
type pinSAB struct {
	mu   sync.Mutex
	adds int
	srv  *httptest.Server
}

func newPinSAB(t *testing.T) *pinSAB {
	t.Helper()
	p := &pinSAB{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".nzb") {
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`))
			return
		}
		if r.URL.Query().Get("mode") == "addfile" {
			p.mu.Lock()
			p.adds++
			p.mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "nzo_ids": []string{"nzo-1"}})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pinSAB) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adds
}

// pinFixture is a queue handler with a public client (priority 1, so the
// default pick) and a private client (priority 50), plus a usenet indexer.
type pinFixture struct {
	h        *QueueHandler
	database *sql.DB
	ctx      context.Context
	clients  *db.DownloadClientRepo
	indexers *db.IndexerRepo
	downl    *db.DownloadRepo
	book     *models.Book
	pub      *pinSAB
	priv     *pinSAB
	pubC     *models.DownloadClient
	privC    *models.DownloadClient
	idx      *models.Indexer
	relURL   string
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	t.Cleanup(httpsec.AllowLoopbackForTests())
	h, database, downloads, clients, books, ctx := queueFixture(t)
	indexers := db.NewIndexerRepo(database)
	h.WithIndexers(indexers)

	pub, priv := newPinSAB(t), newPinSAB(t)
	// The indexer's own host serves the release, so the host match fallback
	// that signs a download URL can find it.
	rel := newPinSAB(t)

	pubHost, pubPort := testServerHostPort(t, pub.srv.URL)
	pubC := &models.DownloadClient{Name: "sab-public", Type: "sabnzbd", Host: pubHost, Port: pubPort, Enabled: true, Priority: 1}
	if err := clients.Create(ctx, pubC); err != nil {
		t.Fatalf("create public client: %v", err)
	}
	privHost, privPort := testServerHostPort(t, priv.srv.URL)
	privC := &models.DownloadClient{Name: "sab-private", Type: "sabnzbd", Host: privHost, Port: privPort, Enabled: true, Priority: 50}
	if err := clients.Create(ctx, privC); err != nil {
		t.Fatalf("create private client: %v", err)
	}

	idx := &models.Indexer{Name: "TrackerX", Type: "newznab", URL: rel.srv.URL + "/api", APIKey: "secret", Enabled: true}
	if err := indexers.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	authors := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "pin-author", Name: "Pin Author", SortName: "Author, Pin", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{ForeignID: "pin-book", AuthorID: author.ID, Title: "Pinned Book", SortTitle: "pinned book", MediaType: models.MediaTypeEbook}
	if err := books.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}
	return &pinFixture{
		h: h, database: database, ctx: ctx, clients: clients, indexers: indexers, downl: downloads, book: book,
		pub: pub, priv: priv, pubC: pubC, privC: privC, idx: idx, relURL: rel.srv.URL + "/rel.nzb",
	}
}

// asAdmin gives a request the role the auth middleware stamps on an admin (and
// on every request when auth is disabled), which is what lets an error name the
// pinned client.
func asAdmin(r *http.Request) *http.Request {
	return r.WithContext(auth.WithUserRole(r.Context(), "admin"))
}

// pin sets the indexer's download client in storage.
func (f *pinFixture) pin(t *testing.T, clientID *int64) {
	t.Helper()
	f.idx.DownloadClientID = clientID
	if err := f.indexers.Update(f.ctx, f.idx); err != nil {
		t.Fatalf("pin indexer: %v", err)
	}
}

func (f *pinFixture) grabBody(guid string, indexerID *int64) []byte {
	body := map[string]any{
		"guid": guid, "title": "Pin Author - Pinned Book epub", "nzbUrl": f.relURL,
		"size": 100, "bookId": f.book.ID, "protocol": "usenet",
	}
	if indexerID != nil {
		body["indexerId"] = *indexerID
	}
	b, _ := json.Marshal(body)
	return b
}

func (f *pinFixture) doGrab(guid string, indexerID *int64) *httptest.ResponseRecorder {
	// Calls grab and writeGrabError the way Grab does, minus the search
	// result registry check that a role bearing caller is held to, which is
	// not what these tests are about.
	var req grabRequest
	_ = json.Unmarshal(f.grabBody(guid, indexerID), &req)
	r := asAdmin(httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", nil))
	w := httptest.NewRecorder()
	if _, err := f.h.grab(r.Context(), req); err != nil {
		writeGrabError(w, r, err)
		return w
	}
	w.WriteHeader(http.StatusAccepted)
	return w
}

func (f *pinFixture) downloadFor(t *testing.T, guid string) *models.Download {
	t.Helper()
	d, err := f.downl.GetByGUID(f.ctx, guid)
	if err != nil || d == nil {
		t.Fatalf("download %s: %v", guid, err)
	}
	return d
}

// TestGrab_UnpinnedIndexerRoutesAsBefore is the regression guard: an indexer
// with no pin selects its client exactly as it did before #3080, which is the
// highest priority enabled client of the protocol.
func TestGrab_UnpinnedIndexerRoutesAsBefore(t *testing.T) {
	f := newPinFixture(t)
	w := f.doGrab("g-unpinned", &f.idx.ID)
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 1 || f.priv.count() != 0 {
		t.Fatalf("unpinned grab: public=%d private=%d, want 1 and 0", f.pub.count(), f.priv.count())
	}
	if got := f.downloadFor(t, "g-unpinned").DownloadClientID; got == nil || *got != f.pubC.ID {
		t.Errorf("DownloadClientID = %v, want the public client %d", got, f.pubC.ID)
	}
}

// TestGrab_PinnedIndexerGoesToItsClient: the pin wins over priority, and the
// download row records the pinned client.
func TestGrab_PinnedIndexerGoesToItsClient(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	w := f.doGrab("g-pinned", &f.idx.ID)
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("pinned grab: public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
	if got := f.downloadFor(t, "g-pinned").DownloadClientID; got == nil || *got != f.privC.ID {
		t.Errorf("DownloadClientID = %v, want the pinned client %d", got, f.privC.ID)
	}
}

// TestGrab_PinWinsOverMediaTypeRouting: an audiobook release would normally
// prefer a client with an audiobook category. The pin overrides that.
func TestGrab_PinWinsOverMediaTypeRouting(t *testing.T) {
	f := newPinFixture(t)
	f.pubC.CategoryAudiobook = "audio"
	if err := f.clients.Update(f.ctx, f.pubC); err != nil {
		t.Fatal(err)
	}
	f.pin(t, &f.privC.ID)
	body, _ := json.Marshal(map[string]any{
		"guid": "g-audio", "title": "Pin Author - Pinned Book m4b", "nzbUrl": f.relURL,
		"size": 100, "bookId": f.book.ID, "protocol": "usenet", "indexerId": f.idx.ID, "mediaType": "audiobook",
	})
	w := httptest.NewRecorder()
	f.h.Grab(w, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", bytes.NewReader(body)))
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
}

// TestGrab_PinFoundByHostMatchStillApplies: a caller that omits the indexer id
// is attributed to an indexer by the download URL's host, and that indexer's
// pin must apply even though the id was not on the request.
func TestGrab_PinFoundByHostMatchStillApplies(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	w := f.doGrab("g-hostmatch", nil)
	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Fatalf("grab: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
}

// TestGrab_PinnedClientDisabledFailsWithoutFallback: the whole point of the
// feature. The other client is enabled and would take the release, and must not.
func TestGrab_PinnedClientDisabledFailsWithoutFallback(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.privC.Enabled = false
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	w := f.doGrab("g-disabled", &f.idx.ID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	msg := w.Body.String()
	for _, want := range []string{"TrackerX", "sab-private", "disabled"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("a grab with its pinned client disabled reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
	if strings.Contains(msg, "no_download_client") {
		t.Errorf("a pin failure must not carry the no_download_client setup nudge code: %s", msg)
	}
}

// TestGrab_PinnedClientWrongProtocolFails: a torrent client pinned to a usenet
// indexer, changed behind the pin's back, fails the grab and sends nothing.
func TestGrab_PinnedClientWrongProtocolFails(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.privC.Type = "qbittorrent"
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	w := f.doGrab("g-proto", &f.idx.ID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"TrackerX", "sab-private", "torrent", "usenet"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("message %q does not mention %q", w.Body.String(), want)
		}
	}
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
}

// TestGrab_PinnedClientMissingFails: a pin that points at a client row that is
// gone (foreign keys off, as a restored backup or manual edit could leave it).
func TestGrab_PinnedClientMissingFails(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	conn, err := f.database.Conn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The in memory database has one connection, so it must be handed back
	// before the grab runs or the grab waits on it forever.
	if _, err := conn.ExecContext(f.ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(f.ctx, "DELETE FROM download_clients WHERE id=?", f.privC.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(f.ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	w := f.doGrab("g-missing", &f.idx.ID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "TrackerX") || !strings.Contains(w.Body.String(), "no longer exists") {
		t.Errorf("message %q should name the indexer and say the client no longer exists", w.Body.String())
	}
	if f.pub.count() != 0 {
		t.Fatalf("fell back to the public client")
	}
}

// TestGrab_PinnedClientUnreachableNamesIndexerAndClient: an enabled pinned
// client that does not answer fails the grab, names both, and does not try the
// other client.
func TestGrab_PinnedClientUnreachableNamesIndexerAndClient(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.priv.srv.Close()
	w := f.doGrab("g-down", &f.idx.ID)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"TrackerX", "sab-private", "no other client"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("message %q does not mention %q", w.Body.String(), want)
		}
	}
	if f.pub.count() != 0 {
		t.Fatalf("fell back to the public client")
	}
}

// TestGrab_PinFailureDoesNotNameClientToNonAdmin: indexers and clients are
// admin managed, so a non-admin's error names the indexer (it is in their
// search results) but not the client.
func TestGrab_PinFailureDoesNotNameClientToNonAdmin(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.privC.Enabled = false
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithUserRole(auth.WithUserID(f.ctx, 7), "user")
	_, err := f.h.grab(ctx, grabRequest{GUID: "g-nonadmin", Title: "Pin Author - Pinned Book epub", NZBURL: f.relURL, Size: 1, Protocol: "usenet", IndexerID: &f.idx.ID})
	if err == nil {
		t.Fatal("expected the grab to fail")
	}
	w := httptest.NewRecorder()
	writeGrabError(w, httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx), err)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "TrackerX") {
		t.Errorf("message %q should name the indexer", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sab-private") {
		t.Errorf("message %q leaks the client name to a non-admin", w.Body.String())
	}
}

// TestRetryAndRegrab_UsePin: a failed row for a pinned indexer is re-sent to
// the pinned client, both by the single retry route and by the bulk retry.
func TestRetryAndRegrab_UsePin(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	dl := &models.Download{
		GUID: "g-retry", Title: "Pin Author - Pinned Book epub", NZBURL: f.relURL, Size: 100,
		Status: models.StateFailed, Protocol: "usenet", Quality: "epub", IndexerID: &f.idx.ID,
		BookID: &f.book.ID, ErrorMessage: "boom",
	}
	if err := f.downl.Create(f.ctx, dl); err != nil {
		t.Fatalf("create failed download: %v", err)
	}
	w := httptest.NewRecorder()
	f.h.RetryDownload(w, asAdmin(retryDownloadRequest(t, dl.ID)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("retry: public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
	if got := f.downloadFor(t, "g-retry").DownloadClientID; got == nil || *got != f.privC.ID {
		t.Errorf("DownloadClientID = %v, want %d", got, f.privC.ID)
	}
}

// TestRetry_PinnedClientDisabledFails: the same retry with the pinned client
// disabled fails and sends nothing.
func TestRetry_PinnedClientDisabledFails(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	f.privC.Enabled = false
	if err := f.clients.Update(f.ctx, f.privC); err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{
		GUID: "g-retry2", Title: "Pin Author - Pinned Book epub", NZBURL: f.relURL, Size: 100,
		Status: models.StateFailed, Protocol: "usenet", Quality: "epub", IndexerID: &f.idx.ID, BookID: &f.book.ID,
	}
	if err := f.downl.Create(f.ctx, dl); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.h.RetryDownload(w, asAdmin(retryDownloadRequest(t, dl.ID)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("retry: status %d, want 400: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 0 {
		t.Fatalf("reached a client: public=%d private=%d", f.pub.count(), f.priv.count())
	}
}

// TestPendingGrab_UsesPin: a release held by a delay profile, force grabbed
// from the Pending tab, goes to the pinned client.
func TestPendingGrab_UsesPin(t *testing.T) {
	f := newPinFixture(t)
	f.pin(t, &f.privC.ID)
	pending := db.NewPendingReleaseRepo(f.database)
	releaseJSON, _ := json.Marshal(map[string]any{
		"guid": "g-pending", "title": "Pin Author - Pinned Book epub", "nzbUrl": f.relURL,
		"size": 100, "protocol": "usenet", "indexerId": f.idx.ID,
	})
	if err := pending.Upsert(f.ctx, &models.PendingRelease{
		BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: "Pin Author - Pinned Book epub",
		IndexerID: &f.idx.ID, GUID: "g-pending", Protocol: "usenet", Reason: "delay", ReleaseJSON: string(releaseJSON),
	}); err != nil {
		t.Fatal(err)
	}
	all, err := pending.List(f.ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("pending list: %v (%d rows)", err, len(all))
	}
	id := strconv.FormatInt(all[0].ID, 10)
	ph := NewPendingHandler(pending, f.h, f.h.downloads, f.h.books)
	w := httptest.NewRecorder()
	ph.Grab(w, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/pending/"+id+"/grab", nil), "id", id))
	if w.Code != http.StatusCreated {
		t.Fatalf("pending grab: status %d: %s", w.Code, w.Body.String())
	}
	if f.pub.count() != 0 || f.priv.count() != 1 {
		t.Fatalf("public=%d private=%d, want 0 and 1", f.pub.count(), f.priv.count())
	}
}
